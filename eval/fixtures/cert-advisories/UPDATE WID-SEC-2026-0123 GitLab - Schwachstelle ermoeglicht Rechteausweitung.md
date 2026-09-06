# WID-SEC-2026-0123: GitLab - Schwachstelle ermoeglicht Rechteausweitung

CVSS Base: 7.8 | Classification: hoch
CVEs: CVE-2026-10123, CVE-2026-10124
Affected products: GitLab CE/EE 17.0.x
Published: 2026-08-29
Update: Zweiter, verwandter CVE fuer denselben Codepfad ergaenzt.

## Produktbeschreibung
GitLab ist eine webbasierte DevOps-Plattform fuer Versionsverwaltung, CI/CD und Projektmanagement, die haeufig als zentrales Code-Repository eingesetzt wird.

## Angriffsbeschreibung
Ein lokaler, niedrig privilegierter Angreifer kann eine Schwachstelle in GitLab ausnutzen, um seine Rechte auf dem betroffenen System auszuweiten. Ursache ist eine fehlerhafte Berechtigungspruefung in einer internen Komponente. Update vom 2026-08-29: Zweiter, verwandter CVE fuer denselben Codepfad ergaenzt.

## Betroffene Produkte
- GitLab CE/EE 17.0.x

## CVEs
- CVE-2026-10123
- CVE-2026-10124

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0123
