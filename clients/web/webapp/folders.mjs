import { compareCodePoints, isWorking, newestFirst, projectName } from './search.mjs';

// All's Folders tree, by the shared rules in api/testdata/clients/README.md: repos A to Z, each with
// its main checkout's sessions and then its worktrees A to Z, sessions newest first. `project` keeps
// only the sessions with that full project path. Working here ignores activity.
export function folderTree(sessions, now, project = null) {
  const shown = sessions.filter((s) => project == null || s.project === project);
  const newest = newestFirst(now);
  const working = (s) => isWorking(s, null);
  // Lower-cased first, then as written, both by code point.
  const lowerFirst = (a, b) => compareCodePoints(a.toLowerCase(), b.toLowerCase()) || compareCodePoints(a, b);
  const top = [...shown].sort(newest)[0];

  const byRepo = new Map();
  for (const s of shown) {
    const path = s.folder?.repoPath ?? s.project;
    if (!byRepo.has(path)) byRepo.set(path, []);
    byRepo.get(path).push(s);
  }
  const repos = [...byRepo].map(([repoPath, members]) => {
    const ordered = members.sort(newest);
    const byWorktree = new Map();
    for (const s of ordered) {
      const name = s.folder?.worktree;
      if (name == null) continue;
      if (!byWorktree.has(name)) byWorktree.set(name, []);
      byWorktree.get(name).push(s);
    }
    const worktrees = [...byWorktree].map(([name, list]) => ({
      name, count: list.length, working: list.filter(working).length, open: list.includes(top), sessions: list,
    })).sort((a, b) => lowerFirst(a.name, b.name));
    return {
      repoPath, name: ordered[0].folder?.repo ?? projectName(repoPath), count: ordered.length,
      working: ordered.filter(working).length, open: ordered.includes(top),
      sessions: ordered.filter((s) => s.folder?.worktree == null), worktrees,
    };
  });
  return repos.sort((a, b) => compareCodePoints(a.name.toLowerCase(), b.name.toLowerCase()) || compareCodePoints(a.repoPath, b.repoPath));
}
