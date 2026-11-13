// Setup profiles describe authoring clients, independently of worker adapters.
// Versions pin fixture expectations; they do not claim a real CLI was executed.
export const profiles = [
  { id: 'claude-2.1.319', name: 'claude', version: '2.1.319', config: '.mcp.json', skill: '.claude/skills/muxlio/SKILL.md' },
  { id: 'codex-0.167.0', name: 'codex', version: '0.167.0', config: '.codex/config.toml', skill: '.agents/skills/muxlio/SKILL.md' },
  { id: 'cursor-2.4.0', name: 'cursor', version: '2.4.0', config: '.cursor/mcp.json', skill: '.cursor/skills/muxlio/SKILL.md' },
  { id: 'cursor-2.3.0', name: 'cursor', version: '2.3.0', config: '.cursor/mcp.json', skill: '.cursor/rules/muxlio.mdc', projectOnly: true },
  { id: 'kiro-2.37.0', name: 'kiro', version: '2.37.0', config: '.kiro/settings/mcp.json', skill: '.kiro/skills/muxlio/SKILL.md' },
];

export function selectProfiles(id) {
  if (!id) return profiles;
  if (id === 'antigravity-unavailable') return [];
  const selected = profiles.filter(p => p.id === id);
  if (!selected.length) throw new Error(`client-configuration: unknown acceptance profile ${id}`);
  return selected;
}
