import { describe, expect, it } from 'vitest';
import { parseDiff } from './diff';

const PATCH = `diff --git a/README.md b/README.md
index 3b18e51..9daeafb 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,4 @@
 # demo

 A tiny sample repository.
+Hello
`;

describe('parseDiff', () => {
  it('parses a single-file patch with correct add/del counts', () => {
    const files = parseDiff(PATCH);
    expect(files).toHaveLength(1);
    const f = files[0]!;
    expect(f.path).toBe('README.md');
    expect(f.additions).toBe(1);
    expect(f.deletions).toBe(0);
  });

  it('assigns line numbers to context and added lines', () => {
    const f = parseDiff(PATCH)[0]!;
    const added = f.lines.find((l) => l.type === 'add');
    expect(added?.text).toBe('Hello');
    expect(added?.newNo).toBe(4);
    const context = f.lines.filter((l) => l.type === 'context');
    expect(context[0]?.oldNo).toBe(1);
    expect(context[0]?.newNo).toBe(1);
  });

  it('handles multi-file patches', () => {
    const multi = `diff --git a/x.txt b/x.txt
--- a/x.txt
+++ b/x.txt
@@ -1 +1 @@
-old
+new
diff --git a/y.txt b/y.txt
--- a/y.txt
+++ b/y.txt
@@ -0,0 +1 @@
+created
`;
    const files = parseDiff(multi);
    expect(files.map((f) => f.path)).toEqual(['x.txt', 'y.txt']);
    expect(files[0]!.additions).toBe(1);
    expect(files[0]!.deletions).toBe(1);
    expect(files[1]!.additions).toBe(1);
  });

  it('parses a plain unified diff without diff --git header', () => {
    const plain = `--- a/f.txt
+++ b/f.txt
@@ -1 +1,2 @@
 keep
+add
`;
    const files = parseDiff(plain);
    expect(files).toHaveLength(1);
    expect(files[0]!.additions).toBe(1);
  });

  it('returns empty array for empty input', () => {
    expect(parseDiff('')).toEqual([]);
  });
});
