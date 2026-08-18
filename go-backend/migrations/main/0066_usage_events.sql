-- +goose Up
-- Usage-Ledger fuer beantwortete Turns auf ALLEN Oberflaechen
-- (docs/superpowers/specs/2026-08-17-usage-events-design.md). Vorher haben
-- internal/openaicompat und internal/mcpserver gar nichts persistiert: nur
-- internal/chat/store_pg.go schreibt chats/messages. Ein OpenWebUI- oder
-- MCP-Client konnte also beliebig LLM-Budget verbrauchen, waehrend jeder
-- Zaehler und jeder "Letzte Aktivitaet"-Zeitstempel in JustRAG stehen blieb.
--
-- Eine Zeile = ein AKZEPTIERTER Turn (nach KB-/User-Aufloesung und
-- Body-Validierung, vor der Antwort). Ein Turn, der danach scheitert, zaehlt
-- weiterhin — er hat das Modell-Budget verbraucht, und genau deshalb sind
-- diese Zahlen mit der litellm-Abrechnung vergleichbar.
CREATE TABLE IF NOT EXISTS usage_events (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Alle FKs sind ON DELETE SET NULL, nicht CASCADE: das ist ein Ledger.
    -- Loeschen einer KB, eines Users oder eines API-Keys darf die
    -- Gesamtsumme nicht rueckwirkend schrumpfen. Pro-KB-Aggregate ignorieren
    -- NULL. internal/cascade braucht deshalb keine Aenderung.
    kb_id      uuid REFERENCES knowledge_bases(id) ON DELETE SET NULL,
    user_id    uuid REFERENCES users(id)           ON DELETE SET NULL,
    api_key_id uuid REFERENCES api_keys(id)        ON DELETE SET NULL,
    surface    varchar(20) NOT NULL,
    created_at timestamp NOT NULL DEFAULT now()
);

ALTER TABLE usage_events DROP CONSTRAINT IF EXISTS usage_events_surface_check;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_surface_check
    CHECK (surface IN ('web','api_v1','openai_compat','mcp'));

-- kb_id + created_at: die Pro-KB-Aggregate in internal/adminkboverview und
-- internal/kb (kbStatsCols LATERAL). created_at allein: die globalen
-- 24h-Zaehler in internal/systemhealth. Plain CREATE INDEX ist hier korrekt —
-- die Tabelle ist in diesem Moment leer (siehe migrations/README.md, die
-- CONCURRENTLY-Regel gilt fuer schon grosse Tabellen).
CREATE INDEX IF NOT EXISTS usage_events_kb_created_idx ON usage_events (kb_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_events_created_idx    ON usage_events (created_at DESC);

-- Backfill historischer Turns aus der Nachrichtenhistorie.
--
-- Jede Zeile wird 'web' etikettiert, weil `messages` NICHTS traegt, was einen
-- Web-Turn von einem /api/v1-Turn unterscheidet: publicapi ruft dasselbe
-- AddMessage und erzeugt gewoehnliche type='chat'-Zeilen. Historischer
-- openai_compat- und mcp-Verkehr ist unrettbar — er wurde nie irgendwo
-- geschrieben. Ab dem Deployment ist die Zuordnung exakt.
--
-- c.type = 'chat' schliesst Research-Sessions aus: das sind keine Chat-Turns
-- und sie werden auch kuenftig nicht gezaehlt (Non-Goals im Design-Doc).
--
-- Das NOT EXISTS macht den Backfill idempotent. goose fuehrt eine angewandte
-- Migration nicht erneut aus, aber migrations/README.md verlangt, dass ein
-- Re-Run ein No-Op ist — ohne den Guard wuerde er die Zeilen verdoppeln.
INSERT INTO usage_events (kb_id, user_id, surface, created_at)
SELECT c.kb_id, c.user_id, 'web', m.created_at
FROM messages m
JOIN chats c ON c.id = m.chat_id
WHERE m.role = 'user'
  AND c.type = 'chat'
  AND NOT EXISTS (SELECT 1 FROM usage_events);

-- +goose Down
DROP TABLE IF EXISTS usage_events;
