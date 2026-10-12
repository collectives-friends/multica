# UI/UX map

## Where things live

| Layer | Path | Rules of thumb |
| --- | --- | --- |
| Primitives + tokens | `packages/ui/` | no business logic, no `@multica/core` imports |
| Shared pages/components | `packages/views/` | no `next/*`, no `react-router-dom`, no store definitions; use `NavigationAdapter`, `useNavigation()`, `<AppLink>` |
| Web routes | `apps/web/app/` + `apps/web/platform/` | framework APIs stay here |
| Desktop | `apps/desktop/src/renderer/src/` | `routes.tsx`, `pages/`, `platform/`, `stores/` |
| Mobile | `apps/mobile/` | independent client; follow `apps/mobile/AGENTS.md` |
| Component docs | `packages/ui/docs/*.md`, `apps/ui-lab/` | Button and Dialog contracts are authoritative |

## Primitives and tokens

- Prefer existing shadcn/Base UI primitives; add with `pnpm ui:add <component>`. Vendored `@reui/*` components are adapted into `packages/ui/components/ui/` and compositions into `packages/views/`.
- Typography uses the role-named scale in `packages/ui/styles/tokens.css` (`--text-micro`, `--text-caption`, `--text-label`, `--text-body`, `--text-body-lg`, `--text-title-sm`, `--text-title`, `--text-title-lg`, `--text-display-sm`, `--text-display`), not Tailwind's default size ramp. Colors come from the shared semantic tokens in `packages/ui/styles/`.
- Selected states stay identifiable on hover. Handle overflow, long text, and scrolling deliberately; avoid unnecessary local state and dividers.

## Copy and i18n

- Descriptions are optional and omitted by default. State each fact once beside the relevant control; keep permissions, cost, destructive consequences, execution prerequisites, and error recovery visible when relevant.
- Translation keys: three-level `feature.component.action`; plurals `_one`/`_other`; interpolation `{{var}}`. Glossary and Chinese voice rules: `apps/docs/content/docs/developers/conventions.mdx`.
- Runtime display names: never render `runtime.name` in UI. Use `runtimeDisplayLabel(runtime)`, `runtimeDisplayName(runtime)`, `runtimeRowLabel(runtime, machineTitle)`, and group with `buildRuntimeMachines`.
- Issue keys (`MUL-123`) are routable identifiers; do not renumber or fabricate them.

## Desktop and mobile specifics

- Desktop: full-window views mount `<DragStrip />` from `@multica/views/platform` as the first flex child; interactive controls in the top 48px need `WebkitAppRegion: "no-drag"`. Pre-workspace one-shot flows (create workspace, accept invite) use `WindowOverlay`, not new routes. Cross-workspace navigation goes through the adapter's `switchWorkspace(slug, targetPath)`.
- Mobile: sheet/navigation selection, data-layer helpers, realtime rules, and verification live in `apps/mobile/AGENTS.md`. Mobile has its own strings; web/desktop locale files do not cover it.
