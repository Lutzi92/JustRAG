# WID-SEC-2026-0111: OpenSSL - Schwachstelle ermoeglicht Ausfuehrung beliebigen Codes

CVSS Base: 9.8 | Classification: kritisch
CVEs: CVE-2026-10111
Affected products: OpenSSL 3.2.x
Published: 2026-07-03

## Produktbeschreibung
OpenSSL ist eine quelloffene Bibliothek fuer Transport Layer Security (TLS) und allgemeine kryptographische Funktionen, die von zahlreichen Anwendungen und Betriebssystemen zur Absicherung der Netzwerkkommunikation eingesetzt wird.

## Angriffsbeschreibung
Ein entfernter, nicht authentisierter Angreifer kann durch das Senden speziell praeparierter Anfragen an OpenSSL beliebigen Code mit den Rechten des betroffenen Dienstes ausfuehren. Die Schwachstelle beruht auf einer unzureichenden Eingabevalidierung.

## Betroffene Produkte
- OpenSSL 3.2.x

## CVEs
- CVE-2026-10111

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0111
