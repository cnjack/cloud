/*
 * diff.ts — parse a unified diff (git patch) into per-file hunks with typed
 * lines, for the hand-rolled DiffView renderer. No external dep.
 */

export type DiffLineType = 'add' | 'del' | 'context' | 'hunk' | 'meta';

export interface DiffLine {
  type: DiffLineType;
  text: string;
  /** Old-file line number (context/del). */
  oldNo?: number;
  /** New-file line number (context/add). */
  newNo?: number;
}

export interface DiffFile {
  /** Best-guess display path (new path, falling back to old). */
  path: string;
  oldPath?: string;
  newPath?: string;
  lines: DiffLine[];
  additions: number;
  deletions: number;
}

const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

export function parseDiff(patch: string): DiffFile[] {
  const files: DiffFile[] = [];
  let current: DiffFile | null = null;
  let oldNo = 0;
  let newNo = 0;
  // Lines before the first @@ of a file are git headers (index/mode/rename/…),
  // not diff-body lines. Only inside a hunk do +/-/space lines count.
  let inHunk = false;

  const push = (line: DiffLine) => {
    if (current) current.lines.push(line);
  };

  const lines = patch.split('\n');
  for (const raw of lines) {
    if (raw.startsWith('diff --git')) {
      current = {
        path: '',
        lines: [],
        additions: 0,
        deletions: 0,
      };
      files.push(current);
      inHunk = false;
      push({ type: 'meta', text: raw });
      continue;
    }

    // A patch that isn't wrapped in `diff --git` (e.g. plain unified diff):
    // start a file on the first ---/+++/@@ we see.
    if (!current && (raw.startsWith('--- ') || raw.startsWith('@@'))) {
      current = { path: '', lines: [], additions: 0, deletions: 0 };
      files.push(current);
      inHunk = false;
    }

    if (raw.startsWith('--- ')) {
      const p = raw.slice(4).replace(/^a\//, '').replace(/\t.*$/, '');
      if (current) current.oldPath = p === '/dev/null' ? undefined : p;
      push({ type: 'meta', text: raw });
      continue;
    }
    if (raw.startsWith('+++ ')) {
      const p = raw.slice(4).replace(/^b\//, '').replace(/\t.*$/, '');
      if (current) current.newPath = p === '/dev/null' ? undefined : p;
      push({ type: 'meta', text: raw });
      continue;
    }

    const hunk = HUNK_RE.exec(raw);
    if (hunk) {
      oldNo = Number(hunk[1]);
      newNo = Number(hunk[3]);
      inHunk = true;
      push({ type: 'hunk', text: raw });
      continue;
    }

    if (!current) continue;

    // Before the first hunk: treat everything as a git header (meta).
    if (!inHunk) {
      push({ type: 'meta', text: raw });
      continue;
    }

    if (raw.startsWith('+')) {
      current.additions++;
      push({ type: 'add', text: raw.slice(1), newNo: newNo++ });
    } else if (raw.startsWith('-')) {
      current.deletions++;
      push({ type: 'del', text: raw.slice(1), oldNo: oldNo++ });
    } else if (raw.startsWith('\\')) {
      // "\ No newline at end of file"
      push({ type: 'meta', text: raw });
    } else {
      // context line (leading space) or blank
      const text = raw.startsWith(' ') ? raw.slice(1) : raw;
      push({ type: 'context', text, oldNo: oldNo++, newNo: newNo++ });
    }
  }

  // Resolve display paths.
  for (const f of files) {
    f.path = f.newPath || f.oldPath || '(unknown)';
  }

  return files;
}
