# WID-SEC-2026-0113: Citrix NetScaler - Schwachstelle ermoeglicht Ausfuehrung beliebigen Codes

CVSS Base: 9.8 | Classification: kritisch
CVEs: CVE-2026-10113
Affected products: NetScaler ADC 14.1.x, NetScaler Gateway 14.1.x
Published: 2026-06-08

## Produktbeschreibung
Citrix NetScaler ist eine Application-Delivery- und Load-Balancing-Plattform, die haeufig als Reverse-Proxy und VPN-Gateway vor unternehmenskritischen Anwendungen eingesetzt wird.

## Angriffsbeschreibung
Ein entfernter, nicht authentisierter Angreifer kann durch das Senden speziell praeparierter Anfragen an Citrix NetScaler beliebigen Code mit den Rechten des betroffenen Dienstes ausfuehren. Die Schwachstelle beruht auf einer unzureichenden Eingabevalidierung.

## Betroffene Produkte
- NetScaler ADC 14.1.x
- NetScaler Gateway 14.1.x

## CVEs
- CVE-2026-10113

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0113
