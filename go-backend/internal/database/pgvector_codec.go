package database

import (
	"context"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
)

// This file wires pgvector's binary wire format into pgx. pgvector-go v0.4.0
// ships only the driver-agnostic `pgvector.Vector` type (with EncodeBinary /
// DecodeBinary); the per-driver pgx codec subpackage was removed upstream, and
// the version that still carries it (v0.2.2) drags gorm/ent/bun/go-pg into the
// module graph. So we keep the dependency to the tiny type wrapper and supply
// the ~40-line pgx codec ourselves — the same shape as the upstream v0.2.2
// codec, which still satisfies pgx v5's pgtype.Codec interface unchanged.
//
// With the codec registered on a connection's TypeMap, binding a
// pgvector.Vector as a query parameter sends the 16 KB binary form for a
// 4096-dim vector instead of the ~65 KB text literal formatEmbedding builds,
// and scanning a `vector` column into a *pgvector.Vector decodes binary bytes
// instead of running one strconv.ParseFloat per component. The SQL itself is
// unchanged ($n::vector casts still resolve the parameter OID to `vector`, so
// the query plan is identical — this only changes the parameter transfer
// encoding, never the plan).

// pgvectorRegisterWarnLogged ensures the "vector type registration failed"
// warning fires at most once per process, matching iterativeScanWarnLogged.
// (Absence of the type is NOT an error — see registerPgvectorTypes.)
var pgvectorRegisterWarnLogged atomic.Bool

// registerPgvectorTypes registers the binary `vector` codec on conn's TypeMap
// when the pgvector extension is installed in the connected database. It is
// deliberately fail-open: a database without the extension (e.g. the main DB
// in a two-DB deployment) reports a NULL OID for to_regtype('vector'), in
// which case registration is skipped and the connection stays fully usable
// for non-vector queries. Only `vector` is registered — halfvec/sparsevec are
// never bound or scanned as Go values (they appear only as SQL cast targets),
// so they need no codec.
func registerPgvectorTypes(ctx context.Context, conn *pgx.Conn) {
	var vectorOID *uint32
	if err := conn.QueryRow(ctx, "SELECT to_regtype('vector')::oid").Scan(&vectorOID); err != nil {
		if !pgvectorRegisterWarnLogged.Swap(true) {
			slog.Warn("pgvector type lookup failed; vectors will fall back to text wire format",
				"error", err)
		}
		return
	}
	if vectorOID == nil {
		// Extension not installed on this DB — expected for the main pool in
		// a split main/vector deployment. Nothing to register.
		return
	}
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "vector", OID: *vectorOID, Codec: vectorCodec{}})
}

// vectorCodec teaches pgx to encode/decode pgvector.Vector in both the binary
// (preferred) and text wire formats. Ported from pgvector-go v0.2.2's
// pgx.VectorCodec; the pgtype.Codec interface is unchanged through pgx v5.9.x.
type vectorCodec struct{}

func (vectorCodec) FormatSupported(format int16) bool {
	return format == pgx.BinaryFormatCode || format == pgx.TextFormatCode
}

func (vectorCodec) PreferredFormat() int16 { return pgx.BinaryFormatCode }

func (vectorCodec) PlanEncode(_ *pgtype.Map, _ uint32, format int16, value any) pgtype.EncodePlan {
	if _, ok := value.(pgvector.Vector); !ok {
		return nil
	}
	switch format {
	case pgx.BinaryFormatCode:
		return encodeVectorBinary{}
	case pgx.TextFormatCode:
		return encodeVectorText{}
	}
	return nil
}

type encodeVectorBinary struct{}

func (encodeVectorBinary) Encode(value any, buf []byte) ([]byte, error) {
	return value.(pgvector.Vector).EncodeBinary(buf)
}

type encodeVectorText struct{}

func (encodeVectorText) Encode(value any, buf []byte) ([]byte, error) {
	return append(buf, value.(pgvector.Vector).String()...), nil
}

func (vectorCodec) PlanScan(_ *pgtype.Map, _ uint32, format int16, target any) pgtype.ScanPlan {
	if _, ok := target.(*pgvector.Vector); !ok {
		return nil
	}
	switch format {
	case pgx.BinaryFormatCode:
		return scanVectorBinary{}
	case pgx.TextFormatCode:
		return scanVectorText{}
	}
	return nil
}

type scanVectorBinary struct{}

func (scanVectorBinary) Scan(src []byte, dst any) error {
	return dst.(*pgvector.Vector).DecodeBinary(src)
}

type scanVectorText struct{}

func (scanVectorText) Scan(src []byte, dst any) error {
	return dst.(*pgvector.Vector).Scan(src)
}

func (c vectorCodec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	return c.DecodeValue(m, oid, format, src)
}

func (c vectorCodec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	if src == nil {
		return nil, nil
	}
	var vec pgvector.Vector
	plan := c.PlanScan(m, oid, format, &vec)
	if plan == nil {
		return nil, fmt.Errorf("pgvector: cannot decode vector in format %d", format)
	}
	if err := plan.Scan(src, &vec); err != nil {
		return nil, err
	}
	return vec, nil
}
