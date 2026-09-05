# WID-SEC-2026-0133: Apache Tomcat - Schwachstelle ermoeglicht Ausfuehrung beliebigen Codes

CVSS Base: 9.8 | Classification: kritisch
CVEs: CVE-2026-10133
Affected products: Tomcat 8.5.x
Published: 2026-07-20

## Produktbeschreibung
Apache Tomcat ist ein quelloffener Java-Servlet-Container, der Webanwendungen auf Basis von Java Servlet und JavaServer Pages (JSP) ausfuehrt.

## Angriffsbeschreibung
Ein entfernter, nicht authentisierter Angreifer kann durch das Senden speziell praeparierter Anfragen an Apache Tomcat beliebigen Code mit den Rechten des betroffenen Dienstes ausfuehren. Die Schwachstelle beruht auf einer unzureichenden Eingabevalidierung.

## Betroffene Produkte
- Tomcat 8.5.x

## CVEs
- CVE-2026-10133

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0133
