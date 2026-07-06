/*
 * DiffView — hand-rolled unified-diff renderer (PRD §6 "Diff 视图"). Groups the
 * patch by file, shows +/- gutters with line numbers, and color-codes
 * add/del/hunk/meta lines. Colors are all tokens so the re-skin never touches
 * this file. Includes a "Download .diff" affordance.
 */
import { useMemo } from 'react';
import { parseDiff, type DiffLineType } from '../lib/diff';
import styles from './DiffView.module.css';

// Per-line semantics for assistive tech. Add/del are otherwise conveyed only by
// color + the (decorative) +/- glyph, so screen readers get an explicit label.
const SIGN_LABEL: Partial<Record<DiffLineType, string>> = {
  add: 'added line',
  del: 'removed line',
};

const SIGN_GLYPH: Partial<Record<DiffLineType, string>> = {
  add: '+',
  del: '−',
  hunk: '@',
};

export function DiffView({
  patch,
  downloadUrl,
  downloadName = 'run.diff',
}: {
  patch: string;
  downloadUrl?: string;
  downloadName?: string;
}) {
  const files = useMemo(() => parseDiff(patch), [patch]);
  const totalAdd = files.reduce((n, f) => n + f.additions, 0);
  const totalDel = files.reduce((n, f) => n + f.deletions, 0);

  const empty = !patch.trim();

  return (
    <div className={styles.wrap} data-testid="diff-view">
      <div className={styles.toolbar}>
        <div className={styles.stats}>
          <span className={styles.fileCount}>
            {files.length} file{files.length === 1 ? '' : 's'} changed
          </span>
          <span className={styles.add}>+{totalAdd}</span>
          <span className={styles.del}>−{totalDel}</span>
        </div>
        {downloadUrl && !empty && (
          <a className={styles.download} href={downloadUrl} download={downloadName}>
            Download .diff
          </a>
        )}
      </div>

      {empty ? (
        <div className={styles.empty}>This run produced no changes.</div>
      ) : (
        <div className={styles.files}>
          {files.map((file, i) => (
            <div key={`${file.path}-${i}`} className={styles.file}>
              <div className={styles.fileHead}>
                <span className={styles.filePath}>{file.path}</span>
                <span className={styles.fileStats}>
                  <span className={styles.add}>+{file.additions}</span>
                  <span className={styles.del}>−{file.deletions}</span>
                </span>
              </div>
              <table className={styles.table}>
                <tbody>
                  {file.lines
                    .filter((l) => l.type !== 'meta')
                    .map((line, j) => (
                      <tr key={j} className={styles.line} data-type={line.type}>
                        <td className={styles.lineNo}>{line.oldNo ?? ''}</td>
                        <td className={styles.lineNo}>{line.newNo ?? ''}</td>
                        <td className={styles.sign}>
                          {/* The +/- glyph is decorative (color-duplicated); the
                              visually-hidden label carries the add/del meaning to
                              screen readers. */}
                          {SIGN_LABEL[line.type] && (
                            <span className={styles.srOnly}>{SIGN_LABEL[line.type]}</span>
                          )}
                          <span aria-hidden="true">{SIGN_GLYPH[line.type] ?? ''}</span>
                        </td>
                        <td className={styles.content}>{line.text || ' '}</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
