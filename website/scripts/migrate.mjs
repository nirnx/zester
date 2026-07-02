// One-shot migration: docs/*.md (MkDocs Material) -> content/docs/*.mdx (Fumadocs)
// Usage: node scripts/migrate.mjs
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.resolve(here, '../../docs');
const OUT = path.resolve(here, '../content/docs');

// ---------------------------------------------------------------------------
// Move map: old path (relative to docs/) -> new path (relative to content/docs/)
// ---------------------------------------------------------------------------
const FILE_MOVES = {
  'salt-compatibility.md': 'getting-started/salt-compatibility.mdx',
  'schedule.md': 'guides/scheduling.mdx',
  'sre-runbook.md': 'operations/sre-runbook.mdx',
  'enrollment-operations.md': 'operations/enrollment.mdx',
  'master-api.md': 'reference/master-api.mdx',
  'enrollment-api.md': 'reference/enrollment-api.mdx',
  'enrollment-architecture.md': 'architecture/enrollment.mdx',
  'enrollment-design.md': 'architecture/enrollment-design.mdx',
  'enrollment-security.md': 'architecture/enrollment-security.mdx',
  'architecture-review-2025.md': 'architecture/review-2025.mdx',
};
const DIR_MOVES = {
  'getting-started': 'getting-started',
  states: 'guides/states',
  modules: 'guides/modules',
  settings: 'guides/settings',
  facts: 'guides/facts',
  templating: 'guides/templating',
  targeting: 'guides/targeting',
  basket: 'guides/basket',
  jobs: 'guides/jobs',
  operations: 'operations',
  update: 'operations/update',
  cli: 'reference/cli',
  configuration: 'reference/configuration',
  authentication: 'reference/authentication',
  architecture: 'architecture',
};
const SKIP = new Set(['index.md', 'architecture-review.md']);

function mapPath(rel) {
  if (FILE_MOVES[rel]) return FILE_MOVES[rel];
  const [dir, ...rest] = rel.split('/');
  if (rest.length > 0 && DIR_MOVES[dir] !== undefined) {
    return path.join(DIR_MOVES[dir], rest.join('/')).replace(/\.md$/, '.mdx');
  }
  return null;
}

// route for a new content path, e.g. guides/states/index.mdx -> /docs/guides/states
function routeFor(newRel) {
  let p = newRel.replace(/\.mdx$/, '');
  if (p.endsWith('/index')) p = p.slice(0, -'/index'.length);
  if (p === 'index') p = '';
  return '/docs' + (p ? '/' + p : '');
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
const CALLOUT_TYPE = {
  note: 'info',
  info: 'info',
  tip: 'success',
  success: 'success',
  example: 'info',
  abstract: 'info',
  warning: 'warn',
  caution: 'warn',
  important: 'warn',
  danger: 'error',
  error: 'error',
  bug: 'error',
  failure: 'error',
  question: 'info',
};

function yamlString(s) {
  return JSON.stringify(s);
}

function stripInline(s) {
  return s
    .replace(/`([^`]*)`/g, '$1')
    .replace(/\*\*([^*]*)\*\*/g, '$1')
    .replace(/\*([^*]*)\*/g, '$1')
    .trim();
}

// Escape MDX-hostile characters in a prose line, preserving inline code spans.
function escapeProse(line) {
  const parts = [];
  let rest = line;
  // split into code spans / text, honoring multi-backtick delimiters
  const re = /(`+)([\s\S]*?)\1/g;
  let last = 0;
  let m;
  while ((m = re.exec(line)) !== null) {
    parts.push({ code: false, text: line.slice(last, m.index) });
    parts.push({ code: true, text: m[0] });
    last = m.index + m[0].length;
  }
  parts.push({ code: false, text: line.slice(last) });
  return parts
    .map((p) => {
      if (p.code) return p.text;
      let t = p.text;
      // autolinks <https://x> -> [x](x)
      t = t.replace(/<(https?:\/\/[^>\s]+)>/g, '[$1]($1)');
      t = t.replace(/\{/g, '\\{');
      // escape < when it opens something tag-like
      t = t.replace(/<(?=[A-Za-z/!?])/g, '\\<');
      return t;
    })
    .join('');
}

// ---------------------------------------------------------------------------
// Link rewriting
// ---------------------------------------------------------------------------
function rewriteLinks(line, srcRel, warnings) {
  return line.replace(
    /\]\(([^)#\s]+\.md)(#[^)\s]*)?\)/g,
    (whole, target, anchor = '') => {
      if (/^https?:/.test(target)) return whole;
      const abs = path.normalize(path.join(path.dirname(srcRel), target));
      let mapped;
      if (abs === 'index.md') mapped = 'index.mdx';
      else mapped = mapPath(abs);
      if (!mapped) {
        warnings.push(`${srcRel}: unresolved link ${target}`);
        return whole;
      }
      return `](${routeFor(mapped)}${anchor})`;
    },
  );
}

// ---------------------------------------------------------------------------
// Block-level conversion
// ---------------------------------------------------------------------------
function convertBody(lines, srcRel, warnings) {
  const out = []; // { text, raw } raw=true -> no escaping (code or emitted JSX)
  let i = 0;
  let fence = null; // current fence marker e.g. ```

  const fenceOpen = (l) => {
    const m = l.match(/^(\s*)(```+|~~~+)/);
    return m ? m[2] : null;
  };

  while (i < lines.length) {
    const line = lines[i];

    if (fence) {
      out.push({ text: line, raw: true });
      const f = line.match(/^\s*(```+|~~~+)\s*$/);
      if (f && f[1].startsWith(fence[0]) && f[1].length >= fence.length) fence = null;
      i++;
      continue;
    }
    const fo = fenceOpen(line);
    if (fo) {
      fence = fo;
      out.push({ text: line, raw: true });
      i++;
      continue;
    }

    // --- admonitions: !!! type "Title"   /  ??? type
    let m = line.match(/^(!!!|\?\?\?\+?)\s+([a-zA-Z_+-]+)(?:\s+"([^"]*)")?\s*$/);
    if (m) {
      const type = CALLOUT_TYPE[m[2].toLowerCase()] ?? 'info';
      const title = m[3] ?? m[2].charAt(0).toUpperCase() + m[2].slice(1).toLowerCase();
      const body = [];
      i++;
      while (i < lines.length) {
        const l = lines[i];
        if (l.trim() === '') { body.push(''); i++; continue; }
        if (/^ {4}/.test(l) || /^\t/.test(l)) { body.push(l.replace(/^( {4}|\t)/, '')); i++; continue; }
        break;
      }
      while (body.length && body[body.length - 1] === '') body.pop();
      out.push({ text: `<Callout type="${type}" title=${JSON.stringify(title)}>`, raw: true });
      out.push(...convertBody(body, srcRel, warnings));
      out.push({ text: '</Callout>', raw: true });
      out.push({ text: '', raw: true });
      continue;
    }

    // --- tab groups: === "Title"
    m = line.match(/^===\s+"(.+)"\s*$/);
    if (m) {
      const tabs = [];
      while (i < lines.length) {
        const t = lines[i].match(/^===\s+"(.+)"\s*$/);
        if (!t) break;
        i++;
        const body = [];
        while (i < lines.length) {
          const l = lines[i];
          if (l.trim() === '') { body.push(''); i++; continue; }
          if (/^ {4}/.test(l) || /^\t/.test(l)) { body.push(l.replace(/^( {4}|\t)/, '')); i++; continue; }
          break;
        }
        while (body.length && body[body.length - 1] === '') body.pop();
        tabs.push({ title: stripInline(t[1]), body });
        // skip blank lines between tabs
        let j = i;
        while (j < lines.length && lines[j].trim() === '') j++;
        if (j < lines.length && /^===\s+"/.test(lines[j])) i = j;
      }
      const items = tabs.map((t) => JSON.stringify(t.title)).join(', ');
      out.push({ text: `<Tabs items={[${items}]}>`, raw: true });
      for (const t of tabs) {
        out.push({ text: `<Tab value=${JSON.stringify(t.title)}>`, raw: true });
        out.push(...convertBody(t.body, srcRel, warnings));
        out.push({ text: '</Tab>', raw: true });
      }
      out.push({ text: '</Tabs>', raw: true });
      out.push({ text: '', raw: true });
      continue;
    }

    // --- material card grids
    if (/^<div class="grid cards" markdown>/.test(line)) {
      const block = [];
      i++;
      while (i < lines.length && !/^<\/div>/.test(lines[i])) { block.push(lines[i]); i++; }
      i++; // consume </div>
      out.push(...convertCards(block, srcRel, warnings));
      continue;
    }
    if (/^<div[^>]*markdown>/.test(line) || /^<\/div>/.test(line)) { i++; continue; }

    // --- plain line: strip material leftovers, rewrite links, escape
    let text = line
      .replace(/(\S) --- (\S)/g, '$1 — $2')
      .replace(/:material-[a-z0-9-]+:\{[^}]*\}\s*/g, '')
      .replace(/:material-[a-z0-9-]+:/g, '')
      .replace(/\{\s*\.md-button[^}]*\}/g, '')
      .replace(/\{:?\s*\.[a-zA-Z-][^}]*\}/g, '');
    text = rewriteLinks(text, srcRel, warnings);
    out.push({ text, raw: false });
    i++;
  }
  return out;
}

// Material "grid cards" list -> <Cards>
function convertCards(blockLines, srcRel, warnings) {
  const out = [{ text: '<Cards>', raw: true }];
  let cur = null;
  const flush = () => {
    if (!cur) return;
    const bodyText = cur.body.join(' ').replace(/\s+/g, ' ').trim();
    const title = stripInline(cur.title);
    const attrs = [`title=${JSON.stringify(title)}`];
    if (cur.href) attrs.push(`href=${JSON.stringify(cur.href)}`);
    out.push({ text: `<Card ${attrs.join(' ')}>`, raw: true });
    out.push({ text: escapeProse(rewriteLinks(bodyText, srcRel, warnings)), raw: true });
    out.push({ text: '</Card>', raw: true });
    cur = null;
  };
  for (const l of blockLines) {
    const item = l.match(/^-\s+(.*)$/);
    if (item) {
      flush();
      let head = item[1]
        .replace(/:material-[a-z0-9-]+:\{[^}]*\}\s*/g, '')
        .replace(/:material-[a-z0-9-]+:/g, '')
        .trim();
      let href = null;
      const linkM = head.match(/^\*\*\[([^\]]+)\]\(([^)]+)\)\*\*$/);
      if (linkM) {
        head = linkM[1];
        const rewritten = rewriteLinks(`](${linkM[2]})`, srcRel, warnings);
        href = rewritten.slice(2, -1);
      } else {
        head = head.replace(/^\*\*(.*)\*\*$/, '$1');
      }
      cur = { title: head, href, body: [] };
      continue;
    }
    if (!cur) continue;
    const t = l.trim();
    if (t === '---' || t === '') continue;
    cur.body.push(t);
  }
  flush();
  out.push({ text: '</Cards>', raw: true });
  out.push({ text: '', raw: true });
  return out;
}

// ---------------------------------------------------------------------------
// Per-file conversion
// ---------------------------------------------------------------------------
function convertFile(srcRel, warnings) {
  const raw = fs.readFileSync(path.join(SRC, srcRel), 'utf8');
  let lines = raw.split('\n');

  // strip existing frontmatter
  if (lines[0] === '---') {
    const end = lines.indexOf('---', 1);
    if (end > 0) lines = lines.slice(end + 1);
  }
  // title from first H1
  let title = path.basename(srcRel, '.md');
  const h1 = lines.findIndex((l) => /^# /.test(l));
  if (h1 >= 0) {
    title = stripInline(lines[h1].replace(/^# /, ''));
    lines = [...lines.slice(0, h1), ...lines.slice(h1 + 1)];
  }
  // section index pages: the folder node carries the section name in the
  // sidebar (meta.json title), so the page itself is an overview
  if (path.basename(srcRel) === 'index.md' && srcRel !== 'index.md') {
    title = 'Overview';
  }
  while (lines.length && lines[0].trim() === '') lines.shift();

  // lead paragraph -> description
  let description = null;
  if (
    lines.length &&
    /^[A-Za-z`*[]/.test(lines[0]) &&
    !/^#|^\||^-\s|^!|^>|^```|^===/.test(lines[0])
  ) {
    const para = [];
    let j = 0;
    while (j < lines.length && lines[j].trim() !== '') { para.push(lines[j].trim()); j++; }
    const joined = para.join(' ');
    if (joined.length <= 220 && !joined.includes('](')) {
      description = stripInline(joined).replace(/(\S) --- (\S)/g, '$1 — $2');
      lines = lines.slice(j);
      while (lines.length && lines[0].trim() === '') lines.shift();
    }
  }

  const converted = convertBody(lines, srcRel, warnings);
  const body = converted
    .map((l) => (l.raw ? l.text : escapeProse(l.text)))
    .join('\n');

  const fm = [`title: ${yamlString(title)}`];
  if (description) fm.push(`description: ${yamlString(description)}`);
  return `---\n${fm.join('\n')}\n---\n\n${body.trimEnd()}\n`;
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------
const warnings = [];
let count = 0;
function walk(dir) {
  for (const e of fs.readdirSync(path.join(SRC, dir), { withFileTypes: true })) {
    const rel = dir ? `${dir}/${e.name}` : e.name;
    if (e.isDirectory()) {
      if (e.name === 'stylesheets') continue;
      walk(rel);
      continue;
    }
    if (!e.name.endsWith('.md') || SKIP.has(rel)) continue;
    const dest = mapPath(rel);
    if (!dest) { warnings.push(`no mapping for ${rel}`); continue; }
    const outPath = path.join(OUT, dest);
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(outPath, convertFile(rel, warnings));
    count++;
  }
}
walk('');
console.log(`converted ${count} files`);
if (warnings.length) {
  console.log(`\nWARNINGS (${warnings.length}):`);
  for (const w of warnings) console.log('  ' + w);
}
