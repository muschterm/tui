# Use ACP at the ADE's agent boundary

Reaffirmed 2026-09-22 by [ADR 0014](0014-acp-boundary-official-agent-runtimes.md):
ACP remains the server-to-agent boundary, with official local provider runtimes
behind adapters and a separate application contract for frontends.

The ADE's background application server will act as an Agent Client Protocol (ACP) v1 client, connecting directly to agents that implement ACP and using adapters for other headless agents, including the selected Codex and Claude examples. This keeps provider-specific integration out of the UI and allows another conforming agent to join through the same boundary, at the cost of adapter maintenance and explicitly handling optional or provider-specific capabilities. ACP belongs to the ADE layer, not the reusable shell; shared protocol support does not promise identical capabilities, transferable sessions, or trustworthy turn attribution. The [frontend/server contract](0003-background-server-attachable-clients.md) is separate from ACP.
