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

Pending implementation, automated checks, production rollout and public UI
verification. Each result below must refer to these numbered requirements.
