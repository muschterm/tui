# Terminal UI Foundation

The shared language for the UI specification and its reference applications.

## Language

**Shared specification**:
The common description of user-visible behavior and quality requirements for all three language implementations.
_Avoid_: Shared implementation

**Reference app**:
A working example in one target language that demonstrates the shared specification for future applications.
_Avoid_: Framework, production ADE

**Pane**:
A region of the application's layout, such as the left navigation, central working area, or right sidebar.
_Avoid_: Surface when referring to a layout region

**Surface**:
A task-focused view presented within the application's layout, such as a file browser or Git client.
_Avoid_: Pane when referring to the view's purpose

**Surface type**:
A kind of view available to open, such as Files, Git, or an activity inspector.
_Avoid_: Opened surface when referring to an available choice

**Opened surface**:
An instance of a surface that the user can return to, including while its containing pane is hidden.
_Avoid_: Visible pane when referring to the retained view

**Bottom panel**:
A pane below the primary working area inside the center column that can present a terminal or another surface.
_Avoid_: Terminal when referring to the pane itself

**Agent connection**:
A selectable agent available to the ADE for thread work or a conflict-resolution job.
_Avoid_: Model when referring to the agent as a whole

**Agent development environment (ADE)**:
An application that combines threads with coding agents, work inspection, and development tools in a single workspace.
_Avoid_: Agent terminal host when referring to the complete application

**Thread**:
A continuing body of interaction with one chosen agent, including its messages, activity, and associated work.
_Avoid_: Chat, conversation, session when naming the user-facing entity

**Closed thread**:
A retained thread set aside from open threads, available to reopen with its history and drafts.
_Avoid_: Deleted thread, settled thread

**Thread deletion**:
Permanent removal of a thread and its retained application data.
_Avoid_: Close, hide, archive

**Project**:
A named workspace association used to organize threads and their default checkout.
_Avoid_: Thread, agent connection

**Turn**:
One submitted prompt and the resulting agent work through completion, failure, or cancellation, including its tools and delegated work.
_Avoid_: Tool call when referring to the whole response cycle

**Application server**:
The background owner of running application work and the state that attached frontends observe.
_Avoid_: ACP agent, TUI when referring to this application-owned service

**Frontend**:
A user interface attached to the application server, initially the TUI and potentially a web UI later.
_Avoid_: Agent client when the intended meaning is the user's interface rather than the ACP integration

**Prompt queue**:
Prompts awaiting a turn within a thread.
_Avoid_: Writer queue

**Terminal controller**:
The one attached client currently allowed to send input and set dimensions for an interactive terminal.
_Avoid_: Terminal viewer when referring to control authority

**Presence**:
An indication of another connected client's participation, such as its labelled cursor in a shared file.
_Avoid_: Focus synchronization

**Writer queue**:
Write-capable thread work and conflict-resolution jobs awaiting ownership of a checkout.
_Avoid_: Prompt queue

**Thread context**:
The material currently occupying the agent's context for a thread, together with the applicable capacity.
_Avoid_: Lifetime token usage when referring to current context occupancy

**Subscription quota**:
An allowance and its usage over a provider-defined window, potentially shared by several threads using the same account or plan.
_Avoid_: Thread context limit

**Thread cost**:
The reported or explicitly estimated monetary cost attributable to work in a thread.
_Avoid_: Account spend when the measurement covers only a thread

**Conflict-resolution job**:
A bounded effort to resolve conflicts in an active Git operation, performed manually or with a selected agent.
_Avoid_: Pull when referring to resolution work after integration has stopped

**Activity item**:
A unit of agent work presented in a thread's activity, such as a tool call, plan update, or subagent run.
_Avoid_: Message when referring to operational work rather than a message

**Plan**:
An agent-reported set of work steps and their current states.
_Avoid_: Transcript when referring to the work checklist

**Question request**:
An identified interaction from an agent asking for one or more user answers.
_Avoid_: Prompt when referring to an answer addressed to an existing request

**Blocking question**:
A question whose requesting run waits for its resolution before continuing.
_Avoid_: Paused thread when unrelated delegated work can still proceed

**Asynchronous question**:
A question that stays open for a later answer while its requesting agent continues work.
_Avoid_: Asynchronous callback when describing whether agent execution continues

**Subagent run**:
Work delegated by an agent to a child agent, with its own identity, activity, and available transcript beneath the parent thread.
_Avoid_: Handoff when the parent agent retains the original thread

**Inspector**:
A surface showing the available full details of a selected activity item or subagent run.
_Avoid_: Summary when referring to the detail view
