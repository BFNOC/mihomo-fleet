# Product

## Name

Mihomo Fleet

## Register

product

## Users

One technical operator running several local proxy client instances on a personal workstation. The operator understands ports, configs, and process state, and wants to avoid switching between desktop proxy clients that do not support multi-open workflows cleanly.

## Purpose

Provide a local WebUI for managing multiple mihomo processes. Each instance owns its own proxy ports, local external-controller port, secret, config file, and lifecycle state.

## Principles

- Treat local safety as a first-class product feature: bind control planes to loopback and never assume a public network is safe.
- Keep the interface operational, dense, and direct. Avoid marketing copy and decorative surfaces.
- Make the instance switcher central. Every control and metric must clearly belong to the selected instance.
- Prefer explicit runtime evidence over guessed state: PID, port checks, API health, and process exit status should drive the UI.

## Anti-References

- Desktop proxy clients that assume only one active core.
- Server subscription panels optimized for multi-user VPS sales instead of local process control.
- Dashboards that hide port and config details behind decorative cards.
