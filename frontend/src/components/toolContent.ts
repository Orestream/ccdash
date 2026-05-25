// Parsing for `tool` transcript entries (persisted as "<Tool>: <detail>").

// Tools whose detail is a file path — we show just the basename (the full path
// lives in the title attribute). Other tools (Bash, Grep, …) keep their detail.
const FILE_TOOLS = new Set([
  'Read',
  'Edit',
  'Write',
  'MultiEdit',
  'NotebookEdit',
  'NotebookRead',
]);

// parseToolContent splits a tool message ("Edit: /repo/a.go") into a prominent
// tool name and a detail. For file tools the detail is the basename, with the
// full value returned as `full` for a hover title.
export function parseToolContent(content: string): {
  name: string;
  detail: string;
  full: string;
} {
  const idx = content.indexOf(': ');
  const name = idx === -1 ? content : content.slice(0, idx);
  const full = idx === -1 ? '' : content.slice(idx + 2);
  let detail = full;
  if (full && FILE_TOOLS.has(name)) {
    const slash = full.lastIndexOf('/');
    if (slash !== -1) detail = full.slice(slash + 1);
  }
  return { name, detail, full };
}
