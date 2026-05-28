# JustRAG Frontend

The frontend is a React 19 + Vite 7 application in `web/`.

## Structure

- `src/components/`: feature components, studio UI, admin UI, sidebars, API key UI
- `src/contexts/`: auth, theme, modal, toast, mobile, and KB workspace state
- `src/hooks/`: chat, file management, RSS, Confluence, sharing, version checks, and UI behavior
- `src/routes/`: route-level UI composition
- `src/utils/`: SSE parsing, message tree helpers, error handling, clipboard, viewport

## Commands

Run these from `web/`:

| Command | Purpose |
|---|---|
| `npm run dev` | start the Vite dev server |
| `npm run build` | type-check and build production assets |
| `npm run test` | run frontend tests once |
| `npm run test:watch` | run frontend tests in watch mode |
| `npm run lint` | run ESLint |
| `npm run preview` | preview the production build |

## Development Notes

- The frontend expects the API server to run separately in local development.
- The usual local setup is Vite on `http://localhost:5173` and the Go API on `http://localhost:3000`.
- In production, the built frontend is copied into `/app/client/dist` and served by the Go backend.

## Current Backend Compatibility Notes

The UI still contains Data Explorer and chart-related code paths, but the Go backend currently returns `501` for:

- Data Explorer endpoints (DuckDB not ported)
- chart generation
- image description (`POST /api/describe-image`)

Keep that in mind when changing or documenting frontend behavior.

See the root [README.md](../README.md) and [CONTRIBUTING.md](../CONTRIBUTING.md) for the full project setup.
