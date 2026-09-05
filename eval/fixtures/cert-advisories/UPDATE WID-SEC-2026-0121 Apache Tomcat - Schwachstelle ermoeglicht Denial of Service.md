# WID-SEC-2026-0121: Apache Tomcat - Schwachstelle ermoeglicht Denial of Service

CVSS Base: 6.5 | Classification: mittel
CVEs: CVE-2026-10121
Affected products: Tomcat 11.0.x, Tomcat 10.1.x
Published: 2026-08-12
Update: Zusaetzlicher betroffener Versionszweig Tomcat 9.0.x ergaenzt.

## Produktbeschreibung
Apache Tomcat ist ein quelloffener Java-Servlet-Container, der Webanwendungen auf Basis von Java Servlet und JavaServer Pages (JSP) ausfuehrt.

## Angriffsbeschreibung
Ein entfernter, nicht authentisierter Angreifer kann durch praeparierte Anfragen einen erhoehten Ressourcenverbrauch in Apache Tomcat herbeifuehren. Die Schwachstelle liegt in der Verarbeitung ueberlanger oder fehlerhaft formatierter Eingaben und kann zu einem Absturz oder zur Nichtverfuegbarkeit des betroffenen Dienstes fuehren. Update vom 2026-08-12: Zusaetzlicher betroffener Versionszweig Tomcat 9.0.x ergaenzt.

## Betroffene Produkte
- Tomcat 11.0.x
- Tomcat 10.1.x

## CVEs
- CVE-2026-10121

## Referenzen
- https://cert-fixture.invalid/advisories/wid-sec-2026-0121
