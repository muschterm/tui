package protocol

// Project names a server-side checkout root. Adding one records an existing
// directory; it does not initialize Git, create a worktree or launch work.
type Project struct {
	ID, Name, Path string
}
