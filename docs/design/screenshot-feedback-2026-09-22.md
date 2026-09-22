# Screenshot feedback acceptance

The four annotated user screenshots contain eight requirements. The initial
unannotated redesign reference is a separate fifth image. Prior general design
acceptance did not verify these eight items.

## Cases defined before implementation

1. Conversation title occupies one line, truncates with an ellipsis, and exposes
   the complete title on hover. Check long titles at desktop and mobile widths.
2. A resumable conversation has no duplicate model-switch button in its header.
   Its bottom composer still selects a model and sends that model's catalog ID.
   Non-session retry recovery must remain available when the old model is gone.
3. Conversation composer has one border, with no native fieldset frame.
4. Conversation composer stays at the bottom while history scrolls; short
   conversations also occupy the available height. Check desktop and mobile.
5. Home composer has no outer card border, padding, or shadow around its input.
6. Repository menu paints and receives clicks above every composer toolbar
   control. Selecting a repository still works; mobile navigation stays above it.
7. Home and conversation model pickers render the existing LobeHub provider SVG
   assets, including the actual Zhipu provider, instead of letter placeholders.
8. An empty embedded Kanban has substantial usable column height, with horizontal
   scrolling preserved. Check both empty and populated boards.

## Verification record

Released Console `v0.0.164`, source
`000ef75e5c038d7a88df4bf226ba6ac42dfacf2d`, on 2026-09-22.
Images workflow `35714694791` and Console CI `35714690547` both succeeded.
The Console deployment is ready (1/1) on immutable image
`ghcr.io/cnjack/jcloud-console@sha256:725928dc7601e9efa3537ab4e5f0cc119805eb20fa0679986d6d4dded84711c5`.
Orchestrator and Cube runtime pins remain unchanged on the verified v0.0.159.

Local Console verification: 604 tests across 83 files, typecheck, token lint and
production build passed. The final embedded-only board adjustment additionally
passed all 15 Kanban tests and typecheck. CI verified the final source.
New assertions cover provider SVG rendering and the absence of a duplicate
session-header action while the existing resume test still sends the selected
model's catalog ID. Non-session alternative-model retry tests remain green.

Public browser loaded `index-Cjt1zhde.js`, matching fresh public HTML (HTTP 200,
`Cache-Control: no-cache`). These are production UI observations, not demo data:

| Item | Public verification |
| --- | --- |
| 1 | Original screenshot's run `6356b58c27161e7c312c8a2936cb45bf`: title height changed from 92.39px to 30.80px (one line); computed nowrap/ellipsis and full title attribute verified. Mobile title is one 28px line. |
| 2 | No model-switch button in that session header. Bottom picker opened, selected `qwen3.8-max-preview`, then restored `glm-5.2`, without submitting a conversation. |
| 3 | Session fieldset border changed from 2px to 0px; screenshot confirms only the shared input card border remains. |
| 4 | Desktop history scroll changed from 1153.5px to 0 while dock bottom stayed at 1225px in a 1236px viewport. At 390x844, history scrolled to 1688px while dock bottom stayed at 844px. |
| 5 | Repository composer outer card has computed border 0px, padding 0px and no shadow. Inner input surface remains visible. |
| 6 | Hit-testing the covered toolbar location resolves to the repository menu, and clicking `cnjack/click-share` switches context. Mobile menu stays within 390px and receives clicks; local mobile sidebar navigation also passed. |
| 7 | Home and session GLM pickers contain the existing LobeHub Zhipu SVG. The previous letter fallback is gone. |
| 8 | Screenshot's empty `cnjack/jcode` board: all four columns grew from 85.5px to 611.59px on desktop. Mobile root is 512px, columns 266.5px, with 1220px content scrolling inside 358px width. |

Browser screenshots were saved under `/tmp/cloud-comments-acceptance/`:
`home.png`, `repository-menu.png`, `conversation.png`,
`conversation-mobile.png`, and `board.png`. Browser warning/error capture was
empty at completion. Temporary viewport overrides and the local demo server
were removed. No messages, cards, board bindings, or provider settings were
created or changed for this acceptance.

The original empty board was verified live. The other inspected repositories
had no connected populated board, so populated-board visual acceptance was not
claimed; existing Kanban interaction tests passed. This release addresses the
eight screenshot comments and does not change the separately recorded ChatGPT
provider-region limitation.
