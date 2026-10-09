package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"
)

// PGSessionService is a session.Service on our own Postgres pool (pgx), with
// the semantics of ADK's gorm-based session/database service: app:/user:
// state shared across sessions, temp: state never persisted, optimistic
// staleness check on append.
type PGSessionService struct {
	pool *pgxpool.Pool
}

// NewPGSessionService returns a session service on pool.
func NewPGSessionService(pool *pgxpool.Pool) *PGSessionService {
	return &PGSessionService{pool: pool}
}

var _ session.Service = (*PGSessionService)(nil)

// Create implements session.Service.
func (s *PGSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req.AppName == "" || req.UserID == "" {
		return nil, errors.New("app_name and user_id are required")
	}
	id := req.SessionID
	if id == "" {
		id = platform.NewUUID(ctx)
	}
	now := platform.Now(ctx).Truncate(time.Microsecond)
	appDelta, userDelta, sessDelta := extractStateDeltas(req.State)

	sess := &pgSession{appName: req.AppName, userID: req.UserID, id: id, updatedAt: now}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		appState, err := applyAppDelta(ctx, tx, req.AppName, appDelta, now)
		if err != nil {
			return err
		}
		userState, err := applyUserDelta(ctx, tx, req.AppName, req.UserID, userDelta, now)
		if err != nil {
			return err
		}
		body, err := json.Marshal(sessDelta)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO adk_sessions (app_name, user_id, id, state, update_time) VALUES ($1,$2,$3,$4,$5)`,
			req.AppName, req.UserID, id, body, now); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sess.state = mergeStates(appState, userState, sessDelta)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &session.CreateResponse{Session: sess}, nil
}

// Get implements session.Service.
func (s *PGSessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return nil, fmt.Errorf("app_name, user_id, session_id are required, got %q, %q, %q", req.AppName, req.UserID, req.SessionID)
	}
	var (
		stateRaw []byte
		updated  time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT state, update_time FROM adk_sessions WHERE app_name=$1 AND user_id=$2 AND id=$3`,
		req.AppName, req.UserID, req.SessionID).Scan(&stateRaw, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q: %w", session.ErrNotFound, req.SessionID, err)
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sessState, err := decodeState(stateRaw)
	if err != nil {
		return nil, err
	}

	q := `SELECT body FROM adk_events WHERE app_name=$1 AND user_id=$2 AND session_id=$3`
	args := []any{req.AppName, req.UserID, req.SessionID}
	if !req.After.IsZero() {
		q += ` AND ts >= $4`
		args = append(args, req.After)
	}
	q += ` ORDER BY ts DESC, id DESC`
	if req.NumRecentEvents > 0 {
		q += fmt.Sprintf(` LIMIT %d`, req.NumRecentEvents)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	var evs []*session.Event
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		ev := &session.Event{}
		if err := json.Unmarshal(body, ev); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode event: %w", err)
		}
		evs = append(evs, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(evs)

	appState, err := fetchAppState(ctx, s.pool, req.AppName)
	if err != nil {
		return nil, err
	}
	userState, err := fetchUserState(ctx, s.pool, req.AppName, req.UserID)
	if err != nil {
		return nil, err
	}
	return &session.GetResponse{Session: &pgSession{
		appName: req.AppName, userID: req.UserID, id: req.SessionID,
		state: mergeStates(appState, userState, sessState), events: evs, updatedAt: updated,
	}}, nil
}

// List implements session.Service. Events are not loaded.
func (s *PGSessionService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req.AppName == "" {
		return nil, errors.New("app_name is required")
	}
	q := `SELECT user_id, id, state, update_time FROM adk_sessions WHERE app_name=$1`
	args := []any{req.AppName}
	if req.UserID != "" {
		q += ` AND user_id=$2`
		args = append(args, req.UserID)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	type row struct {
		user, id string
		state    map[string]any
		updated  time.Time
	}
	var found []row
	for rows.Next() {
		var (
			r   row
			raw []byte
		)
		if err := rows.Scan(&r.user, &r.id, &raw, &r.updated); err != nil {
			rows.Close()
			return nil, err
		}
		if r.state, err = decodeState(raw); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	appState, err := fetchAppState(ctx, s.pool, req.AppName)
	if err != nil {
		return nil, err
	}
	userStates := map[string]map[string]any{}
	out := make([]session.Session, 0, len(found))
	for _, r := range found {
		us, ok := userStates[r.user]
		if !ok {
			if us, err = fetchUserState(ctx, s.pool, req.AppName, r.user); err != nil {
				return nil, err
			}
			userStates[r.user] = us
		}
		out = append(out, &pgSession{appName: req.AppName, userID: r.user, id: r.id,
			state: mergeStates(appState, us, r.state), updatedAt: r.updated})
	}
	return &session.ListResponse{Sessions: out}, nil
}

// Delete implements session.Service. Events go with the session (ON DELETE CASCADE).
func (s *PGSessionService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return fmt.Errorf("app_name, user_id, session_id are required, got %q, %q, %q", req.AppName, req.UserID, req.SessionID)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM adk_sessions WHERE app_name=$1 AND user_id=$2 AND id=$3`,
		req.AppName, req.UserID, req.SessionID)
	return err
}

// AppendEvent implements session.Service.
func (s *PGSessionService) AppendEvent(ctx context.Context, cur session.Session, ev *session.Event) error {
	if cur == nil {
		return errors.New("session is nil")
	}
	if ev == nil {
		return errors.New("event is nil")
	}
	if ev.Partial {
		return nil
	}
	if ev.ID == "" {
		ev.ID = platform.NewUUID(ctx)
	}
	ev.Timestamp = ev.Timestamp.Truncate(time.Microsecond)
	sess, ok := cur.(*pgSession)
	if !ok {
		return fmt.Errorf("unexpected session type %T", cur)
	}
	sess.appendEvent(ev)
	ev = trimTempDelta(ev)

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			stateRaw []byte
			updated  time.Time
		)
		err := tx.QueryRow(ctx,
			`SELECT state, update_time FROM adk_sessions WHERE app_name=$1 AND user_id=$2 AND id=$3 FOR UPDATE`,
			sess.appName, sess.userID, sess.id).Scan(&stateRaw, &updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %q, cannot apply event: %w", session.ErrNotFound, sess.id, err)
		}
		if err != nil {
			return fmt.Errorf("append event: %w", err)
		}
		if updated.UnixMicro() > sess.LastUpdateTime().UnixMicro() {
			return fmt.Errorf("stale session error: last update time from request (%s) is older than in database (%s)",
				sess.LastUpdateTime().Format(time.RFC3339Nano), updated.Format(time.RFC3339Nano))
		}
		appDelta, userDelta, sessDelta := extractStateDeltas(ev.Actions.StateDelta)
		if _, err := applyAppDelta(ctx, tx, sess.appName, appDelta, ev.Timestamp); err != nil {
			return err
		}
		if _, err := applyUserDelta(ctx, tx, sess.appName, sess.userID, userDelta, ev.Timestamp); err != nil {
			return err
		}
		sessState, err := decodeState(stateRaw)
		if err != nil {
			return err
		}
		maps.Copy(sessState, sessDelta)
		stateBody, err := json.Marshal(sessState)
		if err != nil {
			return err
		}
		body, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO adk_events (app_name, user_id, session_id, id, ts, body) VALUES ($1,$2,$3,$4,$5,$6)`,
			sess.appName, sess.userID, sess.id, ev.ID, ev.Timestamp, body); err != nil {
			return fmt.Errorf("save event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE adk_sessions SET state=$4, update_time=$5 WHERE app_name=$1 AND user_id=$2 AND id=$3`,
			sess.appName, sess.userID, sess.id, stateBody, ev.Timestamp); err != nil {
			return fmt.Errorf("update session: %w", err)
		}
		sess.setUpdated(ev.Timestamp)
		return nil
	})
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func fetchAppState(ctx context.Context, q querier, app string) (map[string]any, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT state FROM adk_app_states WHERE app_name=$1`, app).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch app state: %w", err)
	}
	return decodeState(raw)
}

func fetchUserState(ctx context.Context, q querier, app, user string) (map[string]any, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT state FROM adk_user_states WHERE app_name=$1 AND user_id=$2`, app, user).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch user state: %w", err)
	}
	return decodeState(raw)
}

// applyAppDelta merges delta into the app state (jsonb ||) and returns the result.
func applyAppDelta(ctx context.Context, tx pgx.Tx, app string, delta map[string]any, at time.Time) (map[string]any, error) {
	if len(delta) == 0 {
		return fetchAppState(ctx, tx, app)
	}
	body, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `INSERT INTO adk_app_states (app_name, state, update_time) VALUES ($1,$2,$3)
		ON CONFLICT (app_name) DO UPDATE SET state = adk_app_states.state || EXCLUDED.state, update_time = EXCLUDED.update_time
		RETURNING state`, app, body, at).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("save app state: %w", err)
	}
	return decodeState(raw)
}

func applyUserDelta(ctx context.Context, tx pgx.Tx, app, user string, delta map[string]any, at time.Time) (map[string]any, error) {
	if len(delta) == 0 {
		return fetchUserState(ctx, tx, app, user)
	}
	body, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `INSERT INTO adk_user_states (app_name, user_id, state, update_time) VALUES ($1,$2,$3,$4)
		ON CONFLICT (app_name, user_id) DO UPDATE SET state = adk_user_states.state || EXCLUDED.state, update_time = EXCLUDED.update_time
		RETURNING state`, app, user, body, at).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("save user state: %w", err)
	}
	return decodeState(raw)
}

func decodeState(raw []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return m, nil
}

func extractStateDeltas(delta map[string]any) (app, user, sess map[string]any) {
	app, user, sess = map[string]any{}, map[string]any{}, map[string]any{}
	for k, v := range delta {
		if c, ok := strings.CutPrefix(k, session.KeyPrefixApp); ok {
			app[c] = v
		} else if c, ok := strings.CutPrefix(k, session.KeyPrefixUser); ok {
			user[c] = v
		} else if !strings.HasPrefix(k, session.KeyPrefixTemp) {
			sess[k] = v
		}
	}
	return app, user, sess
}

func mergeStates(app, user, sess map[string]any) map[string]any {
	out := make(map[string]any, len(app)+len(user)+len(sess))
	maps.Copy(out, sess)
	for k, v := range app {
		out[session.KeyPrefixApp+k] = v
	}
	for k, v := range user {
		out[session.KeyPrefixUser+k] = v
	}
	return out
}

func trimTempDelta(ev *session.Event) *session.Event {
	if len(ev.Actions.StateDelta) == 0 {
		return ev
	}
	filtered := map[string]any{}
	for k, v := range ev.Actions.StateDelta {
		if !strings.HasPrefix(k, session.KeyPrefixTemp) {
			filtered[k] = v
		}
	}
	if len(filtered) == len(ev.Actions.StateDelta) {
		return ev
	}
	cp := *ev
	cp.Actions.StateDelta = filtered
	return &cp
}

// pgSession is the in-process view of a stored session.
type pgSession struct {
	appName, userID, id string

	mu        sync.RWMutex
	events    []*session.Event
	state     map[string]any
	updatedAt time.Time
}

func (s *pgSession) ID() string      { return s.id }
func (s *pgSession) AppName() string { return s.appName }
func (s *pgSession) UserID() string  { return s.userID }

func (s *pgSession) State() session.State {
	s.mu.Lock()
	if s.state == nil {
		s.state = map[string]any{}
	}
	s.mu.Unlock()
	return &pgState{mu: &s.mu, state: s.state}
}

func (s *pgSession) Events() session.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pgEvents(slices.Clone(s.events))
}

func (s *pgSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

func (s *pgSession) setUpdated(t time.Time) {
	s.mu.Lock()
	s.updatedAt = t
	s.mu.Unlock()
}

func (s *pgSession) appendEvent(ev *session.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = map[string]any{}
	}
	maps.Copy(s.state, ev.Actions.StateDelta)
	s.events = append(s.events, trimTempDelta(ev))
}

type pgEvents []*session.Event

func (e pgEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}
func (e pgEvents) Len() int { return len(e) }
func (e pgEvents) At(i int) *session.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}

type pgState struct {
	mu    *sync.RWMutex
	state map[string]any
}

func (s *pgState) Get(k string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.state[k]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *pgState) Set(k string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[k] = v
	return nil
}

func (s *pgState) All() iter.Seq2[string, any] {
	s.mu.RLock()
	cp := maps.Clone(s.state)
	s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range cp {
			if !yield(k, v) {
				return
			}
		}
	}
}
