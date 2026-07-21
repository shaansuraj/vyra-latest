# Production Timeline Implementation Plan

## Summary

Build Timeline as a first-class VYRA tab and keep this file as the source checklist. Items are checked only after the corresponding implementation slice is present and verified.

The implementation is snapshot-first using existing evidence storage, then adds production case/evidence workflows, identity correction, AI-assisted journey reconstruction, topology, proximity, and predictive handoff.

## Checklist

- [x] Create dedicated Timeline sidebar tab and `TabTimeline.tsx`.
- [x] Build three-pane layout: identity/search rail, timeline canvas, evidence drawer.
- [x] Add identity list, selected moments, known/unknown/date/camera filters.
- [x] Add multi-lane timeline by camera, identity, and event type.
- [x] Add snapshot evidence cards with bbox, confidence, severity, camera, timestamp.
- [x] Add journey reconstruction: first seen, last seen, dwell, route, gaps, confidence.
- [x] Add nearby activity windows: 5, 15, and 30 minutes around a sighting.
- [x] Add live follow mode for selected identities.
- [x] Add backend `/timeline/moments` and `/timeline/journey`.
- [x] Add identity correction: assign, merge, split, false-match review.
- [x] Add case workspace: pin moments, notes, owner, reviewed state.
- [x] Add evidence locks and export manifest with hashes.
- [x] Add natural-language timeline search and AI incident narrator.
- [x] Add appearance/photo search using local identity/ReID data.
- [x] Add topology/floorplan model and route arrows.
- [x] Add proximity graph and repeat-unknown analytics.
- [x] Add predictive camera handoff and ghost-trail inference.
- [x] Add privacy-safe reveal mode with audit logging.
- [x] Add Go tests, frontend build checks, and smoke coverage.

## Implementation Changes

- Frontend:
  - Add `timeline` to `SimpleTabType`, sidebar nav, and `Main` switch.
  - Create `TabTimeline.tsx` using existing timeline APIs plus richer production endpoints.
  - Reuse current snapshot auth URL pattern from `TabPersons`/`TabMoments`.
  - Keep UI dense and operational: no landing page, no nested cards, stable timeline lane dimensions, mobile-safe stacked panes.
- Backend:
  - Extend `timeline.go`/`timeline_http.go` with richer query endpoints and response DTOs.
  - Add safe schema migrations in `database.go` for review status, cases, case items, audit log, evidence locks, topology, and export metadata.
  - Add identity actions with auditable assign/merge/split/false-match operations.
  - Add case export that returns evidence package metadata, snapshots, CSV-ready rows, manifest, SHA-256 hashes, and notes.
- Evidence:
  - Use existing snapshot storage for v1 previews.
  - Add rolling clip recorder after snapshot timeline is stable.
  - Default clip window: 15s before and 45s after selected moment.
  - Evidence locks prevent cleanup of referenced snapshots/clips.
- AI/Journey:
  - Add journey API that groups moments by identity, computes route, gaps, dwell, simultaneous sightings, nearby activity, and confidence reasons.
  - Add timeline-scoped Ask AI by extending query request scope, reusing local workers.
  - Add predictive handoff from historical camera transitions and topology adjacency; mark inferred/ghost-trail data clearly.

## Public Interfaces

- Frontend helpers:
  - `fetchTimelineMoments(opts)`
  - `fetchTimelineJourney(globalPersonId, opts)`
  - `backfillTimelineSightings(limit)`
  - `fetchTimelineAppearanceSearch(globalPersonId, opts)`
  - `assignTimelineIdentity(globalPersonId, personId)`
  - `mergeTimelineIdentities(sourceId, targetId)`
  - `splitTimelineMoments(sourceId, momentIds)`
  - `reviewTimelineMoment(momentId, payload)`
  - `auditTimelineReveal(momentId)`
  - `createTimelineCase`, `updateTimelineCase`, `addTimelineCaseItems`, `exportTimelineCase`
- Backend endpoints:
  - `GET /timeline/moments`
  - `GET /timeline/journey`
  - `POST /timeline/backfill`
  - `GET /timeline/appearance-search`
  - `POST /timeline/identities/{id}/assign`
  - `POST /timeline/identities/{id}/merge`
  - `POST /timeline/identities/{id}/split`
  - `POST /timeline/moments/{id}/review`
  - `POST /timeline/moments/{id}/reveal`
  - `GET|POST|PATCH /timeline/cases`
  - `POST /timeline/cases/{id}/items`
  - `POST /timeline/cases/{id}/export`
  - `POST /timeline/evidence-locks`
  - `GET|PATCH /timeline/topology`

## Test Plan

- Go tests for filters, journey ordering, dwell/gap computation, identity assign/merge/split, audit rows, evidence locks, export manifest hashes, and invalid path rejection.
- Frontend `bun run build` plus manual viewport checks for desktop/tablet/mobile.
- Relay tests for new API auth, bad inputs, empty states, pagination, and backward compatibility with existing `/timeline/identities`.
- Smoke test extension in `scripts/e2e_smoke.sh` covers timeline identities, moments, journey, appearance search, case item pinning, evidence locks, and export manifests when authenticated timeline data exists.

## Usage And Troubleshooting

Simple workflow:

1. Open VYRA and select `Timeline`.
2. Use `All`, `Today`, or the search box.
3. If no people appear but Identity has sightings, click `Import`.
4. Pick a person from the left rail.
5. Click a sighting on the timeline.
6. Use the right pane to reveal evidence, mark reviewed, or mark false match.
7. Open `Advanced` only for filters, cases, exports, identity correction, AI narration, topology, proximity, and prediction tools.

Troubleshooting:

- `Import` runs `POST /timeline/backfill` and converts existing identity-bearing agent events into production timeline rows.
- If Timeline is still empty after import, verify that the relay backend is running and that recent agent events include identity data such as `global_person_id`, `person_id`, or person `bboxes` with identity fields.

## Frontend Performance Hardening

- [x] Lazy-load marketing, auth, dashboard shell, live video, and major dashboard tabs instead of eagerly bundling every workspace.
- [x] Lazy-load heavy investigation/control/identity/system subviews behind `Suspense` boundaries.
- [x] Mount only the active `ArkTabs` panel to avoid hidden subtabs doing network and rendering work.
- [x] Make PostHog load dynamically only when configured.
- [x] Load Solid devtools only in development.
- [x] Add Vite manual chunks for Solid, Ark UI, Chart.js, and Three.js.
- [x] Add `/timeline` to the dev proxy so Timeline APIs work in local Vite mode.
- [x] Debounce Timeline filter reloads and ignore stale identity/moment responses.
- [x] Verify production build output has no large-chunk warning; main dashboard chunks are split into route/tab-level assets instead of a single ~1 MB entry.

## Assumptions

- Root `docs/timeline-plan.md` is intentional even though existing docs mostly live under `application/docs`.
- Existing Identity sightings panels remain until Timeline reaches parity; later they can become links into Timeline.
- No cloud map, AI, or search service; all processing remains local/air-gapped.
- Snapshot-first is acceptable for the first complete vertical slice; rolling video clips are added as the evidence layer hardens.
