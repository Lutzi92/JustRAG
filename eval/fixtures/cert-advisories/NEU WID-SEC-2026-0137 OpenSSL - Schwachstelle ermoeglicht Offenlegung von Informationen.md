# WID-SEC-2026-0137: OpenSSL - Schwachstelle ermoeglicht Offenlegung von Informationen

CVSS Base: 5.3 | Classification: mittel
CVEs: CVE-2026-10137
Affected products: OpenSSL 1.1.1.x
Published: 2026-05-11

## Produktbeschreibung
OpenSSL ist eine quelloffene Bibliothek fuer Transport Layer Security (TLS) und allgemeine kryptographische Funktionen, die von zahlreichen Anwendungen und Betriebssystemen zur Absicherung der Netzwerkkommunikation eingesetzt wird.

## Angriffsbeschreibung
Ein entfernter Angreifer kann durch das Senden praeparierter Anfragen an OpenSSL auf Informationen zugreifen, auf die er eigentlich keinen Zugriff haben sollte. Betroffen sind unter anderem interne Konfigurationsdaten und Session-Informationen.

## Betroffene Produkte
- OpenSSL 1.1.1.x

## CVEs
- CVE-2026-10137

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0137
