'use client';
import { useEffect, useState } from 'react';

interface Entry { positionLabel: string; playerName: string; thru: string; toPar?: number | null; points?: number | null; score?: number | null }
interface Board { category: string; division: string; entries: Entry[] }
interface Leaderboard { name: string; final: boolean; currentRound: number; roundStatus: string; updatedAt: string; boards: Board[] }

const toPar = (v?: number | null) => (v == null ? '—' : v > 0 ? `+${v}` : v === 0 ? 'E' : String(v));

/** Polls the public leaderboard every 30 seconds (FR-TRN-08: ≤ 1 minute). */
export function LiveLeaderboard({ lang, propertyId, tournamentId }: { lang: string; propertyId: string; tournamentId: string }) {
  const id = lang === 'id';
  const [lb, setLb] = useState<Leaderboard | null>(null);
  const [error, setError] = useState('');
  const [sel, setSel] = useState(0);
  useEffect(() => {
    let stop = false;
    const load = () => fetch(`/api/v1/public/tournaments/${tournamentId}/leaderboard?propertyId=${propertyId}`).then(async (r) => {
      const d = await r.json().catch(() => ({}));
      if (stop) return;
      if (r.ok) { setLb(d as Leaderboard); setError(''); } else setError(d.detail ?? (id ? 'Leaderboard belum tersedia' : 'The leaderboard is not available'));
    }).catch(() => undefined);
    void load();
    const t = setInterval(load, 30_000);
    return () => { stop = true; clearInterval(t); };
  }, [tournamentId, propertyId, id]);
  if (!lb) return error ? <p className="w-muted">{error}</p> : <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  const b = lb.boards[Math.min(sel, lb.boards.length - 1)];
  const stb = b?.category === 'stableford';
  return (
    <div className="w-card">
      <p>{lb.final ? (id ? 'Hasil akhir' : 'Final results') : `${id ? 'Ronde' : 'Round'} ${lb.currentRound} · ${id ? 'diperbarui' : 'updated'} ${new Date(lb.updatedAt).toLocaleTimeString()}`}</p>
      <label>{id ? 'Papan' : 'Board'}{' '}
        <select value={sel} onChange={(e) => setSel(Number(e.target.value))}>
          {lb.boards.map((x, i) => <option key={i} value={i}>{x.category} · {x.division}</option>)}
        </select>
      </label>
      {b && (
        <table className="w-table">
          <thead><tr><th>Pos</th><th>{id ? 'Pemain' : 'Player'}</th><th>Thru</th><th style={{ textAlign: 'right' }}>{stb ? (id ? 'Poin' : 'Points') : 'To par'}</th></tr></thead>
          <tbody>
            {b.entries.map((e, i) => (
              <tr key={i}><td>{e.positionLabel}</td><td>{e.playerName}</td><td>{e.thru}</td><td style={{ textAlign: 'right' }}>{stb ? e.points ?? '—' : toPar(e.toPar)}</td></tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
