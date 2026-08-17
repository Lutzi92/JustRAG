import type { ChangeEvent } from 'react';
import type { AgentBindingInfo, AgentBindingOption } from '../../../types';
import './NodeBindingControl.css';

// Option value for "no default at all". The empty string is safe as a
// sentinel because every real option encodes a non-empty kind, so no attachable
// agent or team can ever collide with it.
const NONE_VALUE = '';

// Sentinel for a bound agent/team that is NOT in the attachable list — see
// `orphan` below. Deliberately not a legal `kind:id` pair, and the option
// carrying it is disabled, so it can never be submitted.
const ORPHAN_VALUE = '__wf_bound_but_unlisted__';

/** `kind:id`, because an agent id and a team id live in different tables and
 * nothing guarantees they cannot collide. The kind also travels back out on
 * change, since it — not the id — decides which endpoint the write goes to. */
function optionValue(o: AgentBindingOption): string {
  return `${o.kind}:${o.id}`;
}

interface Props {
  binding: AgentBindingInfo;
  /**
   * Non-empty when the control must not be operated right now — a pending
   * settings draft, a save in flight, or this control's own write. The reason
   * is shown to the user, not merely enforced: a dropdown that silently
   * refuses is worse than no dropdown.
   */
  disabledReason?: string;
  /**
   * Called with the chosen option, or null for "Keine Vorgabe". The write
   * itself belongs to the canvas — it owns the error channel and the refetch,
   * and this control deliberately holds NO local copy of the selection: what it
   * displays is always the server's last answer, so a failed write cannot leave
   * it showing a value the KB does not have.
   */
  onChange: (next: AgentBindingOption | null) => void;
}

/**
 * NodeBindingControl is the canvas's first control that is not backed by the
 * site_config registry. The KB's default agent/team lives in
 * agent_kb_links.is_default / team_kb_links.is_default (migration 0061), not in
 * kb_site_configs, so it has no registry key, no origin, and no place in the
 * savebar's batched PUT — see setKbDefaultBinding in ./api.ts for why the write
 * goes to the attach endpoint on change instead.
 *
 * It renders five states:
 *
 *  - a bound agent or team — named, and preselected in the dropdown;
 *  - a bound agent or team that is SWITCHED OFF — named, preselected, and said
 *    so: the link exists, nothing routes through it, and the fix is either to
 *    re-enable it or to clear it here. Filtering it out (which the backend used
 *    to do, by serving the canvas the chat picker's is_enabled-filtered list)
 *    is what made it invisible and unclearable while it quietly waited to take
 *    effect again;
 *  - a bound TEAM with no enabled member — the same "bound but inert" state
 *    reached a different way, and the one nothing on the team itself reveals:
 *    it is switched on, it has a name, and it still takes no turn, because
 *    chat-time resolution answers „empty_team" and falls back to the standard
 *    path. Kept apart from the switched-off case because the remedy is
 *    different — staff the team, do not look for a switch;
 *  - nothing bound — "Keine Vorgabe" preselected;
 *  - UNKNOWN — the server could not read the binding at all. This state gets
 *    NO dropdown. Rendering one would show „Keine Vorgabe" as the selected
 *    entry, which is precisely the claim the backend spent a fourth enum value
 *    avoiding; and there is nothing to operate anyway, since the option list
 *    failed to load with the binding (`options` is empty) and a clear would
 *    have to address a link we cannot name.
 */
export function NodeBindingControl({ binding, disabledReason, onChange }: Props) {
  const unknown = binding.kind === 'unknown';
  const bound = binding.kind === 'agent' || binding.kind === 'team';
  const options = binding.options ?? [];

  // A bound id absent from the attachable list. Off-contract today — the
  // server derives the top-level binding FROM that same list — but if it ever
  // happened, a plain controlled <select> would fall back to its first option
  // and quietly report „Keine Vorgabe" for a KB that has a default. Show it,
  // disabled, instead of guessing on the admin's behalf.
  const orphan = bound && !options.some((o) => o.id === binding.id && o.kind === binding.kind);

  // A bound default whose agent/team is switched off. Kept as its own line
  // rather than folded into the „Aktuell:" text, because it asks the admin for
  // a decision the other states do not: re-enable it, or take it out.
  const boundButOff = bound && binding.disabled;

  // A bound team with nobody in it. Shown only when it is NOT also switched
  // off: two warnings about the same dead binding would read as two problems,
  // and the switched-off text already carries the staffing half in that case.
  const boundButEmpty = bound && binding.emptyTeam && !binding.disabled;

  const currentLine = unknown
    ? 'Aktuell: unbekannt — die Agenten-Verwaltung antwortet gerade nicht.'
    : binding.kind === 'team'
      ? `Aktuell: Team „${binding.name}“`
      : binding.kind === 'agent'
        ? `Aktuell: Agent „${binding.name}“`
        : 'Aktuell: keine Vorgabe — neue Chats starten mit dem normalen Ablauf.';

  // An option that cannot become the default. Both reasons collapse here — the
  // dropdown only needs to know "not selectable" — while the two WARNINGS above
  // stay separate, which is where the difference actually matters to an admin.
  const unusable = (o: AgentBindingOption) => o.disabled || o.emptyTeam;

  const handleChange = (e: ChangeEvent<HTMLSelectElement>) => {
    const raw = e.target.value;
    if (raw === NONE_VALUE) {
      onChange(null);
      return;
    }
    const picked = options.find((o) => optionValue(o) === raw);
    // A value that matches no option can only be the disabled orphan entry,
    // which the browser will not submit anyway. Refusing here rather than
    // asking the server about an id we cannot place.
    //
    // An unusable option is refused for the same reason and not only because
    // the browser would: this is the ONE place an agent or team that cannot run
    // could become a KB default, and the guard belongs where the decision is,
    // not in the markup that happens to make it hard.
    if (picked && !unusable(picked)) onChange(picked);
  };

  return (
    <div className="wf-binding">
      <div className="wf-inspector__section">Vorgabe</div>
      <p className="wf-binding__current" data-kind={binding.kind || 'none'}>{currentLine}</p>

      {boundButOff && (
        <p className="wf-binding__warn" data-testid="wf-binding-disabled">
          {binding.kind === 'team' ? 'Dieses Team ist' : 'Dieser Agent ist'} abgeschaltet
          und übernimmt deshalb nichts — neue Chats starten mit dem normalen Ablauf.
          Schalte {binding.kind === 'team' ? 'das Team' : 'den Agenten'} unter „Agenten
          &amp; Teams“ wieder ein, oder wähle hier „Keine Vorgabe“: sonst greift die
          Vorgabe wieder, sobald jemand {binding.kind === 'team' ? 'das Team' : 'den Agenten'} einschaltet.
        </p>
      )}

      {boundButEmpty && (
        <p className="wf-binding__warn" data-testid="wf-binding-empty-team">
          In diesem Team ist kein einziges Mitglied aktiv — und ohne Mitglied kann das
          Team nichts beantworten. Neue Chats starten deshalb mit dem normalen Ablauf.
          Gib dem Team unter „Agenten &amp; Teams“ mindestens ein aktives Mitglied, oder
          wähle hier „Keine Vorgabe“.
        </p>
      )}

      {unknown ? (
        <p className="wf-binding__note">
          Solange das so ist, lässt sich hier nichts einstellen. Lade die Ansicht neu; bleibt
          es dabei, stimmt etwas mit der Agenten-Verwaltung nicht.
        </p>
      ) : options.length === 0 && !orphan ? (
        <p className="wf-binding__note">
          Dieser Wissensbank ist bisher kein Agent und kein Team zugeordnet — es gibt also
          nichts, was du hier als Vorgabe auswählen könntest.
        </p>
      ) : (
        <label className="wf-binding__row">
          <span className="wf-binding__label">Vorgabe für neue Chats</span>
          <select
            className="wf-binding__select"
            value={orphan ? ORPHAN_VALUE : bound ? `${binding.kind}:${binding.id}` : NONE_VALUE}
            disabled={disabledReason !== undefined}
            onChange={handleChange}
          >
            <option value={NONE_VALUE}>Keine Vorgabe</option>
            {orphan && (
              <option value={ORPHAN_VALUE} disabled>
                {binding.name || binding.id}
              </option>
            )}
            {/* An option that cannot run — switched off, or a team with no
                enabled member — is rendered but not selectable. It has to be
                rendered, because the KB's current default may BE one of them
                and a controlled <select> whose value matches no option falls
                back to its first — which would report „Keine Vorgabe" for a KB
                that has one, i.e. exactly the state this control exists to
                surface. It must not be selectable, because chat-time resolution
                rejects both: binding one would produce a default that never
                applies. */}
            {options.map((o) => (
              <option key={optionValue(o)} value={optionValue(o)} disabled={unusable(o)}>
                {o.kind === 'team' ? `Team: ${o.name}` : `Agent: ${o.name}`}
                {o.disabled ? ' (abgeschaltet)' : o.emptyTeam ? ' (keine aktiven Mitglieder)' : ''}
              </option>
            ))}
          </select>
        </label>
      )}

      {/* Announced, not just enforced: the reason a dropdown is greyed out is
          the only thing that tells an admin what to do about it. */}
      {disabledReason !== undefined && (
        <p className="wf-binding__note" aria-live="polite">{disabledReason}</p>
      )}

      <p className="wf-binding__hint">
        Wird sofort gespeichert — die Vorgabe gehört nicht zu den Einstellungen unten.
      </p>
    </div>
  );
}
