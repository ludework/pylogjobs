# PyLogJobs v2 — Marquis/Medway Transfer Monitor

Webbasiertes Dashboard für Marquis/Medway Transfer-Jobs. Zeigt Cuttern und Admins den Status ihrer Beiträge beim Transfer von CMS/Interplay zum Playout-Server.

Ersatz für die alte `pylogjobs.exe` (Python 2, kompiliert) — neu geschrieben in Go, als einzelne `.exe` ohne Dependencies.

## Features

- **Dashboard** mit Live-Statistiken (aktive Transfers, Erfolge, Fehler, Ø Dauer)
- **Fortschrittsbalken** für laufende Transfers (zeitbasiert + framebasiert)
- **ETA-Anzeige** pro Transfer
- **Frame-Progress-Tracking** aus `Movie duration Change`-Logeinträgen
- **Desktop-Notifications** bei Fehlern (opt-in)
- **Dauer-Spalte** in der Tabelle
- **Quellen-Erkennung:** EDL/FCP, Interplay, Nexio
- **Dark Theme**
- **Deutsche Oberfläche**
- Alle v1-Features: DataTables, Export, Filter, Summary

## Quickstart

1. Repo auf den Marquis-Server klonen/kopieren
2. `pylogjobs.ini` prüfen (Log-Pfad und Port)
3. `start.bat` doppelklicken

```
http://localhost:8023/
```

## Parallel zum Original testen

```cmd
pylogjobs.exe --port 8024
```

Beide lesen nur Logdateien (read-only), keine Konflikte.

## Als Windows-Dienst

```cmd
install_service.bat
nssm start PyLogJobsV2
```

Voraussetzung: [NSSM](https://nssm.cc/) im PATH.

## Konfiguration

`pylogjobs.ini` im selben Ordner wie die exe:

```ini
[pylogjobs]
log_dir = C:\Program Files\Marquis\Logs
port = 8023
```

CLI-Overrides: `pylogjobs.exe --port 8024 --log-dir "D:\Logs"`

## API-Endpoints

| Endpoint                    | Beschreibung                               |
|-----------------------------|--------------------------------------------|
| `/`                         | Web-Dashboard                              |
| `/jobs`                     | DataTables server-side API (v1-kompatibel) |
| `/summary`                  | HTML-Zusammenfassung                       |
| `/api/stats`                | JSON: Dashboard-Statistiken                |
| `/api/active`               | JSON: Laufende Transfers + Frame-Progress  |
| `/api/progress?clip=NAME`   | JSON: Frame-Verlauf für einen Clip         |

## Architektur

```
pylogjobs.exe       Go-Binary, Windows amd64, ~5 MB, keine Dependencies
├── Log Parser      Inkrementell, alle 5s neue Zeilen
├── In-Memory Store Kein SQLite nötig, beim Start frisch geparst
├── HTTP Server     Static Files + JSON/HTML API
└── Transfer-Zuordnung via Thread-ID aus [THREAD:PROCESS]
```

Quellen-Erkennung: `.xml` Dateiref → EDL/FCP, Hex-MobID → Interplay, sonstige → Nexio.

## Neu bauen

Go installieren, dann:

```bash
cd src/
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o ../pylogjobs.exe .
```

## Repo-Struktur

```
pylogjobs.exe           Windows-Binary
pylogjobs.ini           Konfiguration
start.bat               Quick-Start
install_service.bat     NSSM Service-Installation
static/                 Web-Frontend + Assets
  index.html            Dashboard (v2)
  css/                  Bootstrap 3
  js/                   jQuery, Bootstrap
  fonts/                Glyphicons
  DataTables/           DataTables + Plugins
  favicon.ico
src/                    Go-Quellcode
  main.go
  go.mod
```
