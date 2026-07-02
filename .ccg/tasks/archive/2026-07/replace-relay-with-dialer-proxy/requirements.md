# Requirements

- Replace the deprecated mihomo `relay` proxy group used by global-chain mode with `dialer-proxy`.
- Preserve the user-facing chain order: `A`, `B`, `节点选择` means traffic enters through `A`, then `B`, then exits through the node selected in `节点选择`.
- Keep subscription routing disabled in global-chain mode: discard profile `proxy-groups`, `rules`, `rule-providers`, `sub-rules`, and `script`.
- Keep local proxy YAML per instance and do not write generated changes back to the shared profile.
- Preserve chains that start or end with `节点选择`; the last chain member is the final rule target.
