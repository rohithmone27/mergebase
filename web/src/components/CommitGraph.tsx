import type { GraphCommit } from "../types";

// CommitGraph draws the project's real commit DAG: branch lines diverging
// from a shared commit and rejoining at merges.
//
// Layout is a small lane assignment, the same idea a git GUI uses. Commits
// arrive newest-first; each is placed on a row, and lanes are claimed by
// whichever child needed the commit as a parent. A merge commit's second
// parent opens a new lane, which is what makes the divergence visible.

const ROW = 34;
const LANE = 22;
const LEFT = 18;
const TOP = 16;

interface Placed {
  commit: GraphCommit;
  row: number;
  lane: number;
}

function layout(commits: GraphCommit[]): { placed: Placed[]; lanes: number } {
  const placed: Placed[] = [];
  // reserved[lane] = commit id that lane is currently waiting to draw
  const reserved: (string | null)[] = [];

  const claim = (id: string): number => {
    const existing = reserved.indexOf(id);
    if (existing !== -1) return existing;
    const free = reserved.indexOf(null);
    const lane = free === -1 ? reserved.length : free;
    reserved[lane] = id;
    return lane;
  };

  commits.forEach((c, row) => {
    const lane = claim(c.id);
    placed.push({ commit: c, row, lane });
    // This commit is drawn; its lane now carries its first parent, and any
    // second parent opens (or reuses) another lane.
    reserved[lane] = c.parents[0] ?? null;
    for (const p of c.parents.slice(1)) claim(p);
    // Release duplicate lanes pointing at the same parent — two branches
    // that merged share one line from here down.
    for (let i = 0; i < reserved.length; i++) {
      if (reserved[i] && reserved.indexOf(reserved[i]) !== i) reserved[i] = null;
    }
  });

  return { placed, lanes: Math.max(1, ...placed.map((p) => p.lane + 1)) };
}

const LANE_COLORS = ["var(--accent)", "#4ac2a8", "#e0a44a", "#c78be0", "#7cb8f7"];

export function CommitGraph({ commits }: { commits: GraphCommit[] }) {
  if (commits.length === 0) return null;

  const { placed, lanes } = layout(commits);
  const byId = new Map(placed.map((p) => [p.commit.id, p]));
  const width = LEFT + lanes * LANE + 6;
  const height = TOP + placed.length * ROW;

  const x = (lane: number) => LEFT + lane * LANE;
  const y = (row: number) => TOP + row * ROW;
  const color = (lane: number) => LANE_COLORS[lane % LANE_COLORS.length];

  return (
    <div className="card graph-card">
      <header>
        <span>History</span>
        <span className="graph-count">{commits.length} commits</span>
      </header>
      <div className="graph-body">
        <svg
          width={width}
          height={height}
          className="graph-svg"
          role="img"
          aria-label={`Commit graph: ${commits.length} commits across ${lanes} branch lines`}
        >
          {/* edges first, so nodes sit on top */}
          {placed.map(({ commit, row, lane }) =>
            commit.parents.map((parentId, i) => {
              const parent = byId.get(parentId);
              if (!parent) return null;
              const x1 = x(lane);
              const y1 = y(row);
              const x2 = x(parent.lane);
              const y2 = y(parent.row);
              const stroke = color(x1 === x2 ? lane : Math.max(lane, parent.lane));
              const d =
                x1 === x2
                  ? `M${x1} ${y1} L${x2} ${y2}`
                  : // curve out of this lane and into the parent's
                    `M${x1} ${y1} C ${x1} ${y1 + ROW * 0.55}, ${x2} ${y2 - ROW * 0.55}, ${x2} ${y2}`;
              return (
                <path key={commit.id + i} d={d} fill="none" stroke={stroke} strokeWidth={1.6} opacity={0.75} />
              );
            }),
          )}
          {placed.map(({ commit, row, lane }) => (
            <circle
              key={commit.id}
              cx={x(lane)}
              cy={y(row)}
              r={commit.is_merge ? 6 : 4.5}
              fill={commit.is_merge ? "var(--panel)" : color(lane)}
              stroke={color(lane)}
              strokeWidth={commit.is_merge ? 2.5 : 1.5}
            />
          ))}
        </svg>

        <ol className="graph-rows">
          {placed.map(({ commit }) => (
            <li key={commit.id} className="graph-row">
              <span className="graph-msg">
                {commit.is_merge && <span className="merge-tag">MERGE</span>}
                {commit.message}
              </span>
              <span className="graph-meta">
                {commit.heads?.map((h) => (
                  <span key={h} className="head-tag mono">{h}</span>
                ))}
                <span className="mono">{commit.id.slice(0, 7)}</span>
                <span>{commit.author || "someone"}</span>
              </span>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}
