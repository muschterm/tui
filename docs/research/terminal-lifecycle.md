# Terminal close and panel hide

Inspected **2026-09-19** after the user corrected the proposed detach-only terminal close. Sources were read without launching or closing terminals. These are source findings, not runtime tests of either application or this repository.

## T3 Code

Local checkout: `72c44a847c0a76f33b0d21f47548125b7032ec35`.

- The right tab's close action reaches `onCloseSurface` in [RightPanelTabs.tsx:826](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/RightPanelTabs.tsx#L826). [ChatView.tsx:3756](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/ChatView.tsx#L3756) sends `terminal.close` for the surface's terminal identities, with history deletion. T3 can group several terminals in one surface; that grouping is not adopted here.
- The server routes close through [ws.ts:2277](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/server/src/ws.ts#L2277). [Manager.ts:1963](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/server/src/terminal/Manager.ts#L1963) stops the process, removes the session and publishes closure. Its process-stop path uses termination followed by kill escalation; [NodePtyAdapter.ts:89](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/server/src/terminal/NodePtyAdapter.ts#L89) reaches the actual PTY process. Shutdown is asynchronous, so tab removal alone is not proof of process exit.
- The [bottom toggle at ChatView.tsx:3047](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/ChatView.tsx#L3047) changes drawer visibility; the drawer stays mounted while hidden. Separate [close controls at ThreadTerminalDrawer.tsx:1475](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/ThreadTerminalDrawer.tsx#L1475) reach server terminal closure.
- [terminalCloseConfirm.ts:16](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/lib/terminalCloseConfirm.ts#L16) confirms individual close actions when a local dialog API exists. Bulk closes and exit cleanup have different paths. Exact cross-client tab synchronization and detached descendant cleanup were not established.

## Installed Codex desktop code

The installed desktop bundle is `/Applications/ChatGPT.app`, version `26.911.61220`, build `9647`. Its `Contents/Resources/app.asar` contains the Codex terminal frontend. Source inspection read the archive; no bundled code was executed.

In `webview/assets/runtime-6da9b2a9eedd.js`, the shared terminal tab registration supports bottom and right placement. Its `onClose` calls `closeSessionForConversation` for that tab's session. In `app-initial-b21bd554b363.js`, that method verifies the conversation/session association and invokes the manager's close path, which removes the mapping and calls the terminal host service's close method. This establishes a session-close request, not just removal of a visual tab.

`terminal-panel-65b5306da0f4.js` preserves a supplied session across component cleanup instead of closing it solely because its view unmounts. Exact native-host process teardown and any surrounding confirmation UI were not traced or runtime-tested. Runtime module SHA-256: `6c4628b88f3b23ada8b1818ac206d5b08c656b79df19dda2162f2ba4d493c507`.

The [official integrated-terminal documentation](https://learn.chatgpt.com/docs/integrated-terminal) establishes project/worktree scope and the top-right terminal control, but does not specify the full close-versus-hide lifecycle. Do not attribute the source findings to that documentation page.

## Adopted behavior

The user explicitly selected session-ending close: right terminal tab × ends that terminal, panel toggles only hide, and the bottom terminal also has a separate close action. See [the lifecycle contract](../design/layout.md#terminal-close-and-panel-hide). History retention, confirmation details and shutdown implementation follow this project's contracts; T3's history deletion, grouped terminals and error fallback behavior are not automatically additional requirements.
