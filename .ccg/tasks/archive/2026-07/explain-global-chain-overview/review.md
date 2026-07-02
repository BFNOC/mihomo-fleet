# Review

## Finding

- The generated runtime config is already in global-chain mode behavior: `rules` uses `MATCH,hop`, and the local `hop` proxy has `dialer-proxy: 节点选择`.
- The overview field labeled `已保存节点` only displays the selector state, for example `节点选择 -> JP AWS`. It does not represent the generated chain.

## Change

- Added `生效链路` to the overview panel.
- In global-chain mode it renders configured chain order and expands `节点选择` with the saved selected proxy, for example `节点选择（JP AWS） -> hop`.

## Validation

- `rtk node --check internal/app/web/app.js`
- `rtk go test ./...` -> 106 passed in 2 packages

## Note

- No external or multi-model review was run, per user request.
