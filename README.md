# LLM Cockpit

Terminal-Cockpit für mehrere Claude-Code-Sessions (Windows und Linux).

Das Cockpit startet Claude Code headless (`claude -p --input-format stream-json
--output-format stream-json`) und zeigt alle Sessions mit Status, Vorschau,
Kosten, Notizen, Pins und Git-Infos an.

## Bauen und starten

```sh
go build -o cockpit.exe ./cmd/cockpit      # Windows
GOOS=linux go build -o cockpit ./cmd/cockpit
./cockpit.exe
```

Voraussetzung: `claude` und `git` im PATH. Im Cockpit zeigt `?` alle Tastenkürzel.

## Konfiguration

`state.json` im Benutzer-Konfigordner
(`%AppData%\llm-cockpit` bzw. `~/.config/llm-cockpit`):

```json
{
  "config": {
    "claude_bin": "claude",
    "permission_mode": "acceptEdits",
    "ide": "idea"
  }
}
```

`ide` ist der Befehl, der mit dem Ordner aufgerufen wird (z. B. `idea64`, `code`).

## Aufbau

- `internal/core` – Sessions, Claude-Prozesse, stream-json, Transkripte, Speicher (UI-unabhängig)
- `internal/platform` – Git, Explorer, Browser, IDE, Terminal
- `internal/ui` – Bubble-Tea-Oberfläche
