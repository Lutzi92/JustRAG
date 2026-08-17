import { describe, it, expect, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeBindingControl } from './NodeBindingControl';
import type { AgentBindingInfo } from '../../../types';

const TEAM = { kind: 'team' as const, id: 't1', name: 'Recherche-Team', disabled: false, emptyTeam: false };
const AGENT = { kind: 'agent' as const, id: 'a1', name: 'Bibliotheks-Assistent', disabled: false, emptyTeam: false };
// A switched-off agent that is still attached to the KB.
const OFF_AGENT = { kind: 'agent' as const, id: 'off', name: 'Abgeschaltet', disabled: true, emptyTeam: false };
// A team that is switched ON and still cannot answer: nobody in it is enabled.
const EMPTY_TEAM = { kind: 'team' as const, id: 'empty', name: 'Leeres Team', disabled: false, emptyTeam: true };

function info(over: Partial<AgentBindingInfo> = {}): AgentBindingInfo {
  return { kind: '', id: '', name: '', disabled: false, emptyTeam: false, options: [AGENT, TEAM], ...over };
}

type ControlProps = ComponentProps<typeof NodeBindingControl>;

function renderControl(over: Partial<ControlProps> = {}) {
  const props: ControlProps = { binding: info(), onChange: vi.fn(), ...over };
  return { ...render(<NodeBindingControl {...props} />), props };
}

const select = () => screen.getByRole('combobox', { name: 'Vorgabe für neue Chats' }) as HTMLSelectElement;

describe('NodeBindingControl', () => {
  it('names the bound team and preselects it', () => {
    renderControl({ binding: info({ kind: 'team', id: 't1', name: 'Recherche-Team' }) });
    expect(screen.getByText('Aktuell: Team „Recherche-Team“')).toBeInTheDocument();
    expect(select().value).toBe('team:t1');
  });

  it('names the bound agent and preselects it', () => {
    renderControl({ binding: info({ kind: 'agent', id: 'a1', name: 'Bibliotheks-Assistent' }) });
    expect(screen.getByText('Aktuell: Agent „Bibliotheks-Assistent“')).toBeInTheDocument();
    expect(select().value).toBe('agent:a1');
  });

  it('preselects "Keine Vorgabe" when nothing is bound', () => {
    renderControl();
    expect(screen.getByText(/Aktuell: keine Vorgabe/)).toBeInTheDocument();
    expect(select().value).toBe('');
    expect(screen.getByRole('option', { name: 'Keine Vorgabe' })).toBeInTheDocument();
  });

  it('labels each option with what it is, so an agent and a team of the same name stay apart', () => {
    renderControl();
    expect(screen.getByRole('option', { name: 'Team: Recherche-Team' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Agent: Bibliotheks-Assistent' })).toBeInTheDocument();
  });

  it('reports the chosen option, kind included — the kind picks the endpoint, not the id', async () => {
    const onChange = vi.fn();
    renderControl({ onChange });
    await userEvent.selectOptions(select(), 'team:t1');
    expect(onChange).toHaveBeenCalledWith(TEAM);
  });

  it('reports null for "Keine Vorgabe", which is the clear', async () => {
    const onChange = vi.fn();
    renderControl({ binding: info({ kind: 'team', id: 't1', name: 'Recherche-Team' }), onChange });
    await userEvent.selectOptions(select(), '');
    expect(onChange).toHaveBeenCalledWith(null);
  });

  // The state the backend spent a fourth enum value on: the link tables could
  // not be read. Rendering the dropdown here would show „Keine Vorgabe" as the
  // selected entry — which is exactly the claim „unknown" exists to withhold.
  describe('the unknown binding kind', () => {
    it('says it is unknown and offers no dropdown at all', () => {
      renderControl({ binding: info({ kind: 'unknown', options: [] }) });
      expect(screen.getByText(/Aktuell: unbekannt/)).toBeInTheDocument();
      expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    });

    it('never renders "Keine Vorgabe" for it — an unreadable binding is not an absent one', () => {
      renderControl({ binding: info({ kind: 'unknown', options: [] }) });
      expect(screen.queryByText('Keine Vorgabe')).not.toBeInTheDocument();
      expect(screen.queryByText(/Aktuell: keine Vorgabe/)).not.toBeInTheDocument();
    });

    // Off-contract (the handler ships an empty option list with `unknown`), but
    // the decision must not hinge on the list being empty: with nothing bound
    // that we can name, there is no link to write isDefault:false against, so
    // there is no operation to offer even if options did arrive.
    it('stays uneditable even if options somehow arrive alongside it', () => {
      renderControl({ binding: info({ kind: 'unknown' }) });
      expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    });
  });

  it('explains the empty case instead of showing a dropdown with only "Keine Vorgabe"', () => {
    renderControl({ binding: info({ options: [] }) });
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    expect(screen.getByText(/kein Agent und kein Team zugeordnet/)).toBeInTheDocument();
  });

  it('disables the dropdown and says why when a reason is given', () => {
    renderControl({ disabledReason: 'Speichere oder verwirf deine Änderungen, bevor du die Vorgabe änderst.' });
    expect(select()).toBeDisabled();
    expect(screen.getByText('Speichere oder verwirf deine Änderungen, bevor du die Vorgabe änderst.'))
      .toBeInTheDocument();
  });

  // A default pointing at a switched-off agent. The link exists, nothing routes
  // through it, and until this the canvas was served the chat picker's
  // is_enabled-filtered list — so the KB projected „keine Vorgabe", the admin
  // had nothing to clear, and the binding came back the moment anyone
  // re-enabled the agent.
  describe('a bound default that is switched off', () => {
    const offBinding = () =>
      info({ kind: 'agent', id: 'off', name: 'Abgeschaltet', disabled: true, emptyTeam: false,
        options: [AGENT, OFF_AGENT, TEAM] });

    it('names it and preselects it rather than falling back to "Keine Vorgabe"', () => {
      renderControl({ binding: offBinding() });
      expect(screen.getByText('Aktuell: Agent „Abgeschaltet“')).toBeInTheDocument();
      expect(select().value).toBe('agent:off');
    });

    it('says it is switched off and what to do about it', () => {
      renderControl({ binding: offBinding() });
      const warn = screen.getByTestId('wf-binding-disabled');
      expect(warn).toHaveTextContent(/abgeschaltet/);
      // Both ways out must be named — re-enable it, or clear it here. A warning
      // that only states the problem leaves the admin where they started.
      expect(warn).toHaveTextContent(/wieder ein/);
      expect(warn).toHaveTextContent(/Keine Vorgabe/);
    });

    it('can be cleared — that is the whole point of showing it', async () => {
      const onChange = vi.fn();
      renderControl({ binding: offBinding(), onChange });
      await userEvent.selectOptions(select(), '');
      expect(onChange).toHaveBeenCalledWith(null);
    });

    // The hard constraint: a switched-off agent must not become a NEW default.
    // Chat-time resolution filters is_enabled, so binding one would produce a
    // default that silently never applies.
    it('offers switched-off options as unselectable, and refuses one anyway', async () => {
      const onChange = vi.fn();
      renderControl({ binding: info({ options: [AGENT, OFF_AGENT] }), onChange });

      const option = screen.getByRole('option', { name: /Abgeschaltet/ });
      expect(option).toBeDisabled();
      expect(option).toHaveTextContent('(abgeschaltet)');

      // Belt and braces: even if something bypasses the disabled attribute, the
      // change handler must not report it.
      await userEvent.selectOptions(select(), 'agent:off').catch(() => {});
      expect(onChange).not.toHaveBeenCalled();
      // …and the enabled one right next to it still works, so the assertion
      // above cannot be passing because nothing is selectable at all.
      await userEvent.selectOptions(select(), 'agent:a1');
      expect(onChange).toHaveBeenCalledWith(AGENT);
    });

    it('shows no warning when the bound default is enabled', () => {
      renderControl({ binding: info({ kind: 'team', id: 't1', name: 'Recherche-Team' }) });
      expect(screen.queryByTestId('wf-binding-disabled')).not.toBeInTheDocument();
    });
  });

  // The second way a binding exists and never fires. Nothing about the team
  // looks wrong — it is switched on and it has a name — but LoadTeamForChat
  // filters its members by is_enabled, so resolveTeamSelection answers
  // „empty_team" and the KB runs the full standard path. The canvas used to
  // draw such a KB as live: node aktiv, OrchTeam projected, every
  // PrepareChatContext stage demoted to „bedingt" on all three lanes.
  describe('a bound team with no active member', () => {
    const emptyBinding = () =>
      info({ kind: 'team', id: 'empty', name: 'Leeres Team', emptyTeam: true,
        options: [AGENT, EMPTY_TEAM, TEAM] });

    it('names it and preselects it rather than falling back to "Keine Vorgabe"', () => {
      renderControl({ binding: emptyBinding() });
      expect(screen.getByText('Aktuell: Team „Leeres Team“')).toBeInTheDocument();
      expect(select().value).toBe('team:empty');
    });

    it('says the team is unstaffed — and does NOT call it switched off', () => {
      renderControl({ binding: emptyBinding() });
      const warn = screen.getByTestId('wf-binding-empty-team');
      expect(warn).toHaveTextContent(/Mitglied/);
      expect(warn).toHaveTextContent(/Keine Vorgabe/);
      // The wrong instruction, and the one a future editor is most likely to
      // reach for: the team's switch is already on.
      expect(warn).not.toHaveTextContent(/abgeschaltet/);
      expect(screen.queryByTestId('wf-binding-disabled')).not.toBeInTheDocument();
    });

    it('can be cleared', async () => {
      const onChange = vi.fn();
      renderControl({ binding: emptyBinding(), onChange });
      await userEvent.selectOptions(select(), '');
      expect(onChange).toHaveBeenCalledWith(null);
    });

    // The hard constraint, same as for a switched-off entry: an unstaffed team
    // must not become a NEW default, because it would never take a turn.
    it('offers it as unselectable, and refuses it anyway', async () => {
      const onChange = vi.fn();
      renderControl({ binding: info({ options: [AGENT, EMPTY_TEAM] }), onChange });

      const option = screen.getByRole('option', { name: /Leeres Team/ });
      expect(option).toBeDisabled();
      expect(option).toHaveTextContent('(keine aktiven Mitglieder)');

      await userEvent.selectOptions(select(), 'team:empty').catch(() => {});
      expect(onChange).not.toHaveBeenCalled();
      // …and the usable option beside it still works, so the assertion above
      // cannot be passing because nothing is selectable at all.
      await userEvent.selectOptions(select(), 'agent:a1');
      expect(onChange).toHaveBeenCalledWith(AGENT);
    });

    // A team that is BOTH switched off and unstaffed shows ONE warning, not
    // two: it is one dead binding, and the switched-off text is the one that
    // carries both halves of the fix.
    it('shows only the switched-off warning when it is also switched off', () => {
      renderControl({ binding: info({ kind: 'team', id: 'empty', name: 'Leeres Team',
        disabled: true, emptyTeam: true, options: [EMPTY_TEAM] }) });
      expect(screen.getByTestId('wf-binding-disabled')).toBeInTheDocument();
      expect(screen.queryByTestId('wf-binding-empty-team')).not.toBeInTheDocument();
    });

    it('shows no warning when the bound team is staffed', () => {
      renderControl({ binding: info({ kind: 'team', id: 't1', name: 'Recherche-Team' }) });
      expect(screen.queryByTestId('wf-binding-empty-team')).not.toBeInTheDocument();
    });
  });

  // Off-contract today (the server derives the top-level binding from the same
  // list it sends as `options`), but a plain controlled <select> whose value
  // matches no option falls back to its FIRST option — here „Keine Vorgabe" —
  // and would report "no default" for a KB that has one.
  it('shows a bound entry that is missing from the option list rather than falling back to "Keine Vorgabe"', () => {
    renderControl({ binding: info({ kind: 'agent', id: 'gone', name: 'Verschwundener Agent', options: [TEAM] }) });
    expect(select().value).not.toBe('');
    expect(screen.getByRole('option', { name: 'Verschwundener Agent' })).toBeDisabled();
  });
});
