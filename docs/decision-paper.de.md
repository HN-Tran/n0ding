# n0ding Decision Paper

Stand: 2026-07-24

## Kurzentscheidung

n0ding ist als Open-Source-Projekt plausibel, wenn es nicht als generischer
Nexus-Klon gebaut wird, sondern als homelab-native Package Hub:
leichtgewichtig, config-first, Docker-friendly und bewusst auf die wichtigsten
Paket-Workflows fokussiert.

Die Idee ist stark genug für einen technischen Spike. Sie ist noch nicht stark
genug für eine monatelange Umsetzung ohne vorherige Validierung.

Empfehlung: 1–2 Wochen Proof of Concept bauen. Danach anhand klarer Kriterien
entscheiden: weitermachen, Scope schneiden oder beenden.

## Problem

Homelabber, Maker und kleine technische Teams nutzen viele Paketquellen:

- Docker/OCI Images
- npm Packages
- Python Packages via PyPI
- apt/apk Packages
- eigene Build-Artefakte
- teilweise große AI/ML-Artefakte

In der Praxis führt das zu wiederkehrenden Problemen:

- gleiche Artefakte werden auf mehreren Nodes mehrfach aus dem Internet gezogen
- Builds und Deployments hängen an externen Registries
- lokale/private Packages sind über verschiedene Tools verteilt
- Nexus/Artifactory/ProGet wirken für Homelabs schwer, alt oder zu
  enterprise-lastig
- Einzellösungen wie Verdaccio, devpi, Docker Registry, apt-cacher-ng oder Harbor
  lösen jeweils nur einen Teil

Das Kernproblem ist nicht, dass es keine Tools gibt. Das Kernproblem ist, dass
es keine angenehm kleine, moderne, homelab-native Zentrale für mehrere
Artifact- und Package-Flows gibt.

## Zielgruppe

Primäre Zielgruppe:

- Homelab-Betreiber
- Selfhoster
- DevOps-nahe Entwickler
- Maker mit mehreren Nodes, VMs, LXCs oder CI Runnern
- kleine technische Teams, die keine schwere Enterprise-Lösung wollen

Diese Zielgruppe ist technisch stark. Sie braucht keine vereinfachte
App-Plattform. Sie will Kontrolle, klare Konfiguration, Standardprotokolle und
wenig Magie.

## Positionierung

Arbeitsthese:

> Nexus is enterprise artifact management. n0ding is a homelab-native package
> hub.

n0ding soll nicht „mehr als Nexus“ können. Es soll für den kleineren,
selbstverwalteten Kontext besser passen:

- kleine Installation
- schnelle Einrichtung
- deklarative Konfiguration
- gute Defaults
- einfaches Backup/Restore
- gute Client-Setup-Snippets
- Authentik/Authelia/OIDC/LDAP-freundlich
- CLI-first, UI als Observability- und Admin-Hilfe

## Nicht-Ziele

n0ding sollte in Version 1 nicht versuchen:

- Artifactory vollständig zu ersetzen
- alle Package Manager zu unterstützen
- eine große Enterprise-RBAC-Plattform zu werden
- eine Low-Code-App-Plattform zu sein
- Proxmox, Docker Compose, Ansible oder Forgejo zu ersetzen
- eine kommerzielle SaaS-first-Lösung zu sein

## MVP

Der MVP sollte beweisen, dass n0ding mehrere Registry-Typen unter einer
einheitlichen, leichten Betriebslogik sinnvoll zusammenbringt.

Vorgeschlagener MVP:

- Docker/OCI Proxy Cache
- npm Proxy Cache
- PyPI Proxy Cache
- private Publish-Funktion für mindestens ein Ökosystem
- einheitliche Storage-Abstraktion: lokal zuerst, S3-kompatibel später
- einheitliche Auth-Schicht: Basic/Auth Token zuerst, OIDC später
- einfache YAML/TOML-Konfiguration
- minimale Web-UI für Status, Cache-Hits, Speicherverbrauch und
  Repository-Liste
- CLI oder Copy-Paste-Snippets für Client-Konfiguration

Alternative, konservativere MVP-Variante:

- Docker/OCI + npm zuerst
- PyPI erst nach erfolgreichem Proof of Concept

Diese Variante reduziert Protokollrisiko und beschleunigt den ersten nutzbaren
Release.

## Kernfeatures

Version 1 sollte diese Eigenschaften haben:

- Proxy/Mirror für externe Registries
- lokale/private Registry pro unterstütztem Ökosystem
- transparente Standard-Client-Kompatibilität
- Artefakt-Cache mit Retention-Regeln
- Storage-Metadaten und Health Checks
- Export/Backup-freundliche Datenstruktur
- einfache Konfiguration für Docker Compose
- gute Logs und Metriken

Wichtige UX-Idee:

Jedes Repository zeigt direkt die passende Client-Konfiguration:

- Docker daemon registry mirror
- npm `.npmrc`
- pip `index-url`
- CI-Beispiele

Das ist ein echter Vorteil gegenüber schwereren Tools, weil Setup-Friktion für
Homelabber entscheidend ist.

## Architektur-Optionen

### Option A: Eigene Protokollimplementierungen

Vorteile:

- maximale Kontrolle
- einheitliches internes Modell
- klares technisches Profil

Nachteile:

- hohes Risiko
- viele Edge Cases
- große Security- und Kompatibilitätslast
- lange Zeit bis stabil

Bewertung: Für V1 riskant.

### Option B: Intelligenter Reverse Proxy plus Cache

Vorteile:

- schneller Proof of Concept
- weniger Protokolltiefe am Anfang
- gute Validierung des Nutzens

Nachteile:

- private Publish-Funktion schwieriger
- weniger Kontrolle über Metadaten
- später eventuell Architekturwechsel nötig

Bewertung: Gute Spike-Option.

### Option C: Bestehende Komponenten intern orchestrieren

Beispiel: Verdaccio für npm, Docker Registry für OCI, devpi für PyPI,
gemeinsames UI/Config/Auth drumherum.

Vorteile:

- schnelle Funktionsbreite
- weniger Protokollrisiko
- stabilere Bausteine

Nachteile:

- wirkt schnell wie ein Wrapper
- komplexer Betrieb im Inneren
- schwieriger einheitliches Produktgefühl
- Gefahr, doch nur ein Nexus-Klon mit mehreren Diensten zu werden

Bewertung: Für einen Homelab-Hub interessant, aber nur wenn der Betrieb wirklich
einfacher wird.

## Empfohlene technische Richtung

Start mit Option B, aber mit sauberem Pfad zu eigener Implementierung dort, wo
es nötig wird.

Der Spike sollte klären:

- Kann ein einzelner Dienst Docker/OCI, npm und PyPI sinnvoll als Proxy cachen?
- Wie schwer ist korrekte Auth-Weitergabe?
- Wie viel Metadatenlogik muss n0ding selbst verstehen?
- Wie gut funktionieren Standard-Clients ohne Spezialplugins?
- Ist die Performance und Stabilität für Homelabs gut genug?

## Differenzierung gegen Alternativen

### Nexus Repository

Nexus ist mächtig, aber schwer und enterprise-geprägt. n0ding gewinnt nur, wenn
Installation, Konfiguration und Betrieb deutlich angenehmer sind.

### Artifactory

Artifactory ist für die Zielgruppe meist zu groß, zu kommerziell und zu komplex.
n0ding sollte nicht versuchen, Feature-Parität zu erreichen.

### Verdaccio

Verdaccio ist stark für npm, aber kein Multi-Package-Hub. n0ding kann gewinnen,
wenn npm nur ein Teil einer gemeinsamen lokalen Artifact-Zentrale ist.

### devpi

devpi ist stark für Python, aber ebenfalls spezialisiert. n0ding gewinnt über
Multi-Registry-Integration und bessere Homelab-Betriebserfahrung.

### Harbor

Harbor ist stark bei OCI/Container Security, aber schwer und nicht für npm/PyPI
gedacht. n0ding sollte nicht in Security-Scanning als Kernfeature starten.

## Risiken

### Hohes Protokollrisiko

Jede Registry hat eigene Erwartungen, Metadaten, Auth-Flows und Caching-Regeln.
Fehler führen schnell zu kaputten Builds.

Maßnahme: Mit wenigen Ökosystemen starten und echte Standard-Clients testen.

### Hoher Wartungsaufwand

Package Manager ändern Verhalten. n0ding muss langfristig aktuell bleiben.

Maßnahme: klare Compatibility Matrix und automatisierte Integrationstests.

### Security-Vertrauen

Eine Artifact-Zentrale ist Teil der Supply Chain. Nutzer müssen ihr vertrauen.

Maßnahme: simple Architektur, gute Logs, keine undurchsichtige Magie, frühe
Security-Dokumentation.

### Unklare Motivation für Nutzer

Wenn Setup und Betrieb nicht deutlich einfacher als Nexus sind, gibt es wenig
Grund zu wechseln.

Maßnahme: Setup-Zeit als Produktmetrik behandeln.

### Scope Creep

„Nur noch apt, Maven, NuGet, Go, Helm, Hugging Face“ kann das Projekt zerreißen.

Maßnahme: harte Roadmap-Gates und klares Plugin-/Adaptermodell erst nach MVP.

## Go-Kriterien

Nach dem Spike weitermachen, wenn:

- Docker oder npm Proxy Cache mit Standardclients stabil funktioniert
- Setup lokal in unter 10 Minuten möglich ist
- mindestens zwei Ökosysteme unter einer Konfiguration laufen
- Cache-Hits und Storage-Nutzung sichtbar sind
- private Publish-Funktion für ein Ökosystem realistisch wirkt
- das Projekt sich deutlich leichter anfühlt als Nexus

## Kill-Kriterien

Projekt beenden oder stark umschneiden, wenn:

- Standardclients zu viele Sonderfälle erzeugen
- der Dienst nur durch komplexe Workarounds funktioniert
- der Betrieb intern schwerer wird als Nexus
- private Publish-Funktion pro Ökosystem zu teuer wird
- die Differenzierung am Ende nur „Nexus in kleiner“ ist
- nach 1–2 Wochen kein nutzbarer Proxy-Flow funktioniert

## 1–2-Wochen-Spike-Plan

### Woche 1

- Repository-Struktur anlegen
- Runtime wählen
- minimalen HTTP Proxy mit persistentem Cache bauen
- npm Proxy Cache gegen echten npm-Client testen
- Docker/OCI Pull-Flow analysieren und ersten Cache-Flow testen
- einfache Config-Datei einführen

### Woche 2

- zweiten Pakettyp stabilisieren
- Cache-Metadaten und einfache Retention einführen
- minimale Status-UI oder CLI-Status bauen
- Client-Setup-Snippets generieren
- Docker Compose Setup bauen
- Entscheidung anhand Go-/Kill-Kriterien treffen

## Technologie-Vorschlag

Konservative Optionen:

- Go für eine einzelne statische Binary und gute Netzwerk-/Proxy-Eigenschaften
- Rust für Performance und Robustheit, aber mit höheren Umsetzungskosten
- TypeScript/Bun/Node für schnelle Produktentwicklung, aber eventuell weniger
  attraktiv für ein infra-nahes Homelab-Tool

Empfehlung: Go.

Begründung:

- passt zu Infrastruktur-Tools
- einfache Distribution
- gute HTTP- und Proxy-Bibliotheken
- niedrige Runtime-Komplexität
- Homelab-freundlich

## Open-Source-Strategie

n0ding sollte von Anfang an OSS-nativ wirken:

- klare README mit Problem und Nicht-Zielen
- Docker Compose Quickstart
- Beispielkonfigurationen
- Compatibility Matrix
- Architektur-Dokument
- ehrlicher MVP-Status
- keine pseudo-kommerzielle Landingpage

Lizenzvorschlag:

- Apache-2.0 oder MIT für maximale Adoption
- AGPL nur, wenn später SaaS-Abwehr bewusst gewünscht ist

Für dieses Projekt wirkt Apache-2.0 am professionellsten.

## Mögliche spätere Monetarisierung

Nicht Grundlage für V1, aber optional:

- Support für kleine Teams
- Hosted Control Plane für mehrere n0ding-Instanzen
- Enterprise Auth/RBAC/Audit
- Team Policies
- Supply-Chain-Reports
- Managed Artifact Hub

Für Homelabber selbst ist direkte Monetarisierung unwahrscheinlich. Der Wert
liegt eher in Skill-Aufbau, OSS-Reputation und späterer Anschlussfähigkeit.

## Entscheidung

Aktuelle Entscheidung: Spike starten.

Nicht entscheiden: komplette Produktentwicklung.

Nächster konkreter Schritt:

Einen kleinen Go-Prototyp bauen, der mindestens npm oder Docker als Proxy Cache
sauber bedient, inklusive lokaler Cache-Persistenz und
Client-Konfigurationsbeispiel.

Wenn das in 1–2 Wochen nicht überzeugend gelingt, sollte n0ding entweder auf ein
engeres Problem reduziert oder verworfen werden.
