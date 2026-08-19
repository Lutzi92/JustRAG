-- +goose Up
-- Contract-Schritt zum UI-Organisationsrework: entfernt die Spalte, die den
-- studio_config-Mechanismus getragen hat (pro oeffentlicher KB einzelne
-- Workspace-Funktionen abschaltbar). Der Mechanismus wurde entfernt, weil er
-- nicht mehr gebraucht wird — alle Kacheln sind ueberall verfuegbar und nur
-- noch durch echte Feature-Flags wie chat_compare_enabled gesteuert.
--
-- Der Code-seitige Expand-Schritt ist bereits ausgeliefert: seit dem Rework
-- liest und schreibt KEINE Go-Zeile mehr studio_config (internal/kb und
-- internal/adminglobalkbs haben die Struct-Felder, die SELECT-Spalten und die
-- Update-Zweige verloren), und das Frontend kennt weder den Typ noch das Feld.
-- Diese Migration holt nur nach, was Expand/Contract offen gelassen hat.
--
-- Achtung beim Deploy: ein Release, das eine Migration enthaelt, laesst sich
-- nicht durch Zuruecksetzen des Image-Tags allein zurueckrollen (siehe
-- docs/runbooks/release.md). Ein Rollback auf ein Image VOR dem Rework wuerde
-- hier auf eine fehlende Spalte laufen — deshalb gehoert diese Migration in
-- ein Release NACH dem Rework, nicht in dasselbe.
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "studio_config";

-- +goose Down
-- Stellt die Spalte wieder her, damit ein Downgrade auf ein Image vor dem
-- Rework nicht an einer fehlenden Spalte scheitert. Die INHALTE sind
-- verloren — sie waren beim Drop bereits fachlich tot, weil kein Codepfad sie
-- mehr gelesen hat. Ein Downgrade laesst also alle Workspace-Funktionen
-- ueberall aktiv, was dem Verhalten seit dem Rework entspricht.
ALTER TABLE "knowledge_bases" ADD COLUMN IF NOT EXISTS "studio_config" jsonb;
