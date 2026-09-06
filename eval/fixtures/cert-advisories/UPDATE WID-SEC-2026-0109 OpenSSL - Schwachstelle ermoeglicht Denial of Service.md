# WID-SEC-2026-0109: OpenSSL - Schwachstelle ermoeglicht Denial of Service

CVSS Base: 6.5 | Classification: mittel
CVEs: CVE-2026-10109
Affected products: OpenSSL 3.3.x, OpenSSL 3.2.x
Published: 2026-08-17
Update: Zusaetzliche betroffene Version OpenSSL 3.1.x ergaenzt.

## Produktbeschreibung
OpenSSL ist eine quelloffene Bibliothek fuer Transport Layer Security (TLS) und allgemeine kryptographische Funktionen, die von zahlreichen Anwendungen und Betriebssystemen zur Absicherung der Netzwerkkommunikation eingesetzt wird.

## Angriffsbeschreibung
Ein entfernter, nicht authentisierter Angreifer kann durch praeparierte Anfragen einen erhoehten Ressourcenverbrauch in OpenSSL herbeifuehren. Die Schwachstelle liegt in der Verarbeitung ueberlanger oder fehlerhaft formatierter Eingaben und kann zu einem Absturz oder zur Nichtverfuegbarkeit des betroffenen Dienstes fuehren. Update vom 2026-08-17: Zusaetzliche betroffene Version OpenSSL 3.1.x ergaenzt.

## Betroffene Produkte
- OpenSSL 3.3.x
- OpenSSL 3.2.x

## CVEs
- CVE-2026-10109

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0109
