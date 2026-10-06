const pptxgen = require('pptxgenjs');

const pptx = new pptxgen();
pptx.layout = 'LAYOUT_WIDE';
pptx.author = 'SAGE-K8s team';
pptx.subject = 'Final-week six-minute demo deck';
pptx.title = 'SAGE-K8s — Safe Autonomous Kubernetes Remediation';
pptx.company = 'SAGE-K8s';
pptx.lang = 'en-US';
pptx.theme = {
  headFontFace: 'Aptos Display',
  bodyFontFace: 'Aptos',
  lang: 'en-US',
};

const C = {
  navy: '10243E',
  ink: '17202A',
  teal: '087E8B',
  paleTeal: 'DDF2F3',
  orange: 'F28C28',
  paleOrange: 'FFF0DF',
  green: '2E7D5B',
  red: 'B33A3A',
  grey: '667085',
  pale: 'F4F6F8',
  white: 'FFFFFF',
  line: 'C8D0D9',
};

const W = 13.333;
const H = 7.5;

function addChrome(slide, number, section) {
  slide.background = { color: C.white };
  slide.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: W, h: 0.16, line: { color: C.teal, transparency: 100 }, fill: { color: C.teal } });
  slide.addText(section.toUpperCase(), { x: 0.55, y: 7.08, w: 4.2, h: 0.2, fontFace: 'Aptos', fontSize: 8.5, color: C.grey, bold: true, charSpacing: 1.2, margin: 0 });
  slide.addText(String(number).padStart(2, '0'), { x: 12.1, y: 7.02, w: 0.65, h: 0.26, fontFace: 'Aptos', fontSize: 10, color: C.teal, bold: true, align: 'right', margin: 0 });
}

function addTitle(slide, title, subtitle) {
  slide.addText(title, { x: 0.65, y: 0.45, w: 11.9, h: 0.55, fontFace: 'Aptos Display', fontSize: 26, bold: true, color: C.navy, margin: 0, breakLine: false });
  if (subtitle) slide.addText(subtitle, { x: 0.67, y: 1.05, w: 11.7, h: 0.35, fontSize: 12, color: C.grey, margin: 0 });
}

function addBullets(slide, items, x, y, w, h, opts = {}) {
  const runs = [];
  items.forEach((item, i) => {
    runs.push({ text: item, options: { bullet: { indent: 14 }, hanging: 4, breakLine: i < items.length - 1 } });
  });
  slide.addText(runs, { x, y, w, h, fontSize: opts.fontSize || 17, color: opts.color || C.ink, breakLine: false, paraSpaceAfterPt: opts.space || 11, margin: 0.04, valign: 'mid', fit: 'shrink' });
}

function addPill(slide, text, x, y, w, color = C.teal, fill = C.paleTeal) {
  slide.addShape(pptx.ShapeType.roundRect, { x, y, w, h: 0.42, rectRadius: 0.08, line: { color, width: 1 }, fill: { color: fill } });
  slide.addText(text, { x: x + 0.05, y: y + 0.07, w: w - 0.1, h: 0.22, fontSize: 10.5, bold: true, color, align: 'center', margin: 0 });
}

function addBox(slide, title, body, x, y, w, h, accent = C.teal, fill = C.white) {
  slide.addShape(pptx.ShapeType.roundRect, { x, y, w, h, rectRadius: 0.05, line: { color: C.line, width: 1 }, fill: { color: fill }, shadow: { type: 'outer', color: '9AA4B2', opacity: 0.12, blur: 1, angle: 45, distance: 1 } });
  slide.addShape(pptx.ShapeType.rect, { x, y, w: 0.08, h, line: { color: accent, transparency: 100 }, fill: { color: accent } });
  slide.addText(title, { x: x + 0.25, y: y + 0.2, w: w - 0.45, h: 0.35, fontSize: 16, bold: true, color: C.navy, margin: 0 });
  slide.addText(body, { x: x + 0.25, y: y + 0.67, w: w - 0.45, h: h - 0.85, fontSize: 11.5, color: C.ink, margin: 0, valign: 'top', fit: 'shrink' });
}

function addArrow(slide, x1, y1, x2, y2, color = C.grey) {
  slide.addShape(pptx.ShapeType.line, { x: x1, y: y1, w: x2 - x1, h: y2 - y1, line: { color, width: 1.5, beginArrowType: 'none', endArrowType: 'triangle' } });
}

// 1 — Title
{
  const s = pptx.addSlide();
  s.background = { color: C.navy };
  s.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: 0.2, h: H, line: { color: C.orange, transparency: 100 }, fill: { color: C.orange } });
  s.addText('SAGE-K8s', { x: 0.85, y: 1.35, w: 6.8, h: 0.85, fontFace: 'Aptos Display', fontSize: 44, bold: true, color: C.white, margin: 0 });
  s.addText('Safe autonomous remediation for Kubernetes CrashLoopBackOff', { x: 0.9, y: 2.35, w: 8.5, h: 0.9, fontSize: 24, color: 'D7E5EF', bold: false, margin: 0, breakLine: false });
  s.addText('detect  →  classify  →  validate  →  snapshot  →  act  →  verify  →  restore', { x: 0.9, y: 3.75, w: 10.8, h: 0.45, fontSize: 15, color: C.white, bold: true, charSpacing: 0.4, margin: 0 });
  s.addText('Arya Singh  ·  Ananya  ·  Subhashini', { x: 0.9, y: 5.95, w: 6.5, h: 0.35, fontSize: 13, color: 'AFC6D8', margin: 0 });
  addPill(s, '2 reversible actions', 9.65, 1.55, 2.4, C.orange, '3D2C1E');
  addPill(s, '60s verified stability', 9.65, 2.15, 2.4, C.teal, '123C4A');
  addPill(s, 'automatic snapshot restore', 9.65, 2.75, 2.4, 'B9D6C8', '193A34');
  s.addNotes('Opening line: We automate one failure class deeply, with a reversible two-action allowlist and measured safety behavior.');
}

// 2 — Problem
{
  const s = pptx.addSlide(); addChrome(s, 2, 'Problem'); addTitle(s, 'Kubernetes restarts containers. It does not prove recovery.', 'CrashLoopBackOff is a symptom; safe remediation needs diagnosis, reversibility, and verification.');
  addBox(s, 'What Kubernetes already does', 'Restarts failed containers and applies exponential back-off.', 0.75, 1.75, 3.65, 3.35, C.grey, C.pale);
  addBox(s, 'What remains undecided', 'Should we restart one Pod, undo a Deployment revision, or refuse automation?', 4.85, 1.75, 3.65, 3.35, C.orange, C.paleOrange);
  addBox(s, 'What SAGE-K8s adds', 'A validated two-action choice, an exact pre-action snapshot, a continuous health window, and automatic restore on failure.', 8.95, 1.75, 3.65, 3.35, C.teal, C.paleTeal);
  s.addText('API acceptance ≠ workload recovery', { x: 3.2, y: 5.72, w: 6.9, h: 0.55, fontSize: 25, bold: true, color: C.navy, align: 'center', margin: 0 });
}

// 3 — Gap
{
  const s = pptx.addSlide(); addChrome(s, 3, 'Research gap'); addTitle(s, 'The gap is not proposing an action. It is executing safely.', 'An autonomous loop must know when its own intervention made things worse.');
  const xs = [0.8, 3.95, 7.1, 10.25];
  const titles = ['Recommend', 'Constrain', 'Reverse', 'Measure'];
  const bodies = [
    'An LLM can suggest a repair, but a suggestion is not authority to mutate the cluster.',
    'A deterministic validator enforces target semantics and the two-action allowlist.',
    'Every action begins from a Deployment snapshot and can be undone by restoring it.',
    'Enabled and disabled arms separate controller effect from Kubernetes self-recovery.',
  ];
  xs.forEach((x, i) => { addBox(s, titles[i], bodies[i], x, 1.8, 2.45, 3.75, i === 2 ? C.orange : C.teal, C.white); s.addText(String(i + 1), { x: x + 0.75, y: 4.95, w: 0.9, h: 0.7, fontSize: 32, bold: true, color: i === 2 ? C.orange : C.teal, align: 'center', margin: 0 }); });
}

// 4 — Architecture
{
  const s = pptx.addSlide(); addChrome(s, 4, 'Architecture'); addTitle(s, 'One guarded control loop, four evidence boundaries', 'The LLM proposes; deterministic and reversible components control execution.');
  const y = 2.15;
  const boxes = [
    ['Pod controller', 'Detect + collect evidence', 0.55, 2.05, C.navy],
    ['Classifier', 'Raw proposal', 3.05, 2.05, C.teal],
    ['Validator', 'Allowlist + semantic guard', 5.55, 2.05, C.orange],
    ['Incident manager', 'Budget + backoff', 8.05, 2.05, C.navy],
    ['Safety layer', 'Snapshot + verify + restore', 10.55, 2.05, C.teal],
  ];
  boxes.forEach(([t,b,x,yy,a]) => addBox(s,t,b,x,yy,2.15,1.45,a,C.white));
  for (let i=0;i<4;i++) addArrow(s, boxes[i][2] + 2.15, y + 0.72, boxes[i+1][2], y + 0.72, C.grey);
  addBox(s, 'K3s API', 'Pods · Deployments · ReplicaSets', 1.45, 4.65, 3.0, 1.15, C.grey, C.pale);
  addBox(s, 'Durable audit', 'Exact 8-field JSONL on PVC', 5.18, 4.65, 3.0, 1.15, C.orange, C.paleOrange);
  addBox(s, 'Prometheus / Grafana', 'Operational view of the lifecycle', 8.9, 4.65, 3.0, 1.15, C.teal, C.paleTeal);
  addArrow(s, 11.6, 3.5, 3.0, 4.65, C.grey);
  addArrow(s, 11.6, 3.5, 6.65, 4.65, C.orange);
  addArrow(s, 9.55, 3.5, 10.4, 4.65, C.teal);
}

// 5 — Loop
{
  const s = pptx.addSlide(); addChrome(s, 5, 'Control loop'); addTitle(s, 'One incident: decisions narrow before permissions expand', 'Unsafe and unsupported proposals terminate before a snapshot or action consumes budget.');
  const stages = ['DETECT', 'EVIDENCE', 'CLASSIFY', 'VALIDATE', 'SNAPSHOT', 'ACT', 'VERIFY', 'RECOVER / RESTORE'];
  stages.forEach((t, i) => {
    const x = 0.45 + i * 1.57;
    const c = i < 4 ? C.navy : (i < 6 ? C.orange : C.teal);
    s.addShape(pptx.ShapeType.roundRect, { x, y: 2.15, w: 1.28, h: 0.82, rectRadius: 0.04, line: { color: c, width: 1.2 }, fill: { color: C.white } });
    s.addText(t, { x: x + 0.08, y: 2.42, w: 1.12, h: 0.22, fontSize: 9.5, bold: true, color: c, align: 'center', margin: 0, fit: 'shrink' });
    if (i < stages.length - 1) addArrow(s, x + 1.28, 2.56, x + 1.55, 2.56, C.grey);
  });
  addBox(s, 'Safe stop', 'escalated / rejected\nattemptNumber = 0', 3.75, 4.15, 2.55, 1.45, C.red, 'FCE8E8');
  addBox(s, 'Verified success', 'same Pod UID Ready\nfor 60 uninterrupted seconds', 7.15, 4.15, 2.55, 1.45, C.green, 'E3F1EA');
  addBox(s, 'Failed verification', 'restore exact pre-action\nDeployment spec', 10.15, 4.15, 2.55, 1.45, C.orange, C.paleOrange);
  addArrow(s, 6.25, 2.98, 5.0, 4.15, C.red);
  addArrow(s, 11.1, 2.98, 8.4, 4.15, C.green);
  addArrow(s, 11.5, 2.98, 11.5, 4.15, C.orange);
}

// 6 — Safety
{
  const s = pptx.addSlide(); addChrome(s, 6, 'Owner 2'); addTitle(s, 'Safety is a temporal contract, not a single readiness check', 'The verifier attributes one replacement and requires continuous health.');
  s.addShape(pptx.ShapeType.line, { x: 1.0, y: 2.28, w: 11.0, h: 0, line: { color: C.line, width: 3 } });
  const marks = [
    [1.0, 'Snapshot', 'Exact Deployment spec'],
    [3.25, 'Capture UIDs', 'All pre-action Pods'],
    [5.5, 'Dispatch', 'Injected action callback'],
    [7.75, '≤30s', 'First Ready observation'],
    [10.0, '+60s', 'Same UID stays healthy'],
    [12.0, 'Outcome', 'Recover or restore'],
  ];
  marks.forEach(([x,t,b], i) => {
    const color = i === 5 ? C.orange : C.teal;
    s.addShape(pptx.ShapeType.ellipse, { x: x - 0.16, y: 2.12, w: 0.32, h: 0.32, line: { color, width: 1 }, fill: { color } });
    s.addText(t, { x: x - 0.65, y: 2.65, w: 1.3, h: 0.3, fontSize: 12, bold: true, color, align: 'center', margin: 0 });
    s.addText(b, { x: x - 0.8, y: 3.03, w: 1.6, h: 0.62, fontSize: 9.5, color: C.grey, align: 'center', margin: 0, fit: 'shrink' });
  });
  addBullets(s, [
    'Fail immediately if Ready flips false, the Pod disappears, UID changes, or named-container restartCount increases.',
    'Never switch candidates after the stability window starts.',
    'Snapshot restoration is the only rollback path; rollout_undo is an injected remediation action.',
    'Every transition is appended to durable JSONL with raw timestamps for later measurement.',
  ], 1.0, 4.15, 11.1, 2.05, { fontSize: 15.5, space: 9 });
}

// 7 — Experiment
{
  const s = pptx.addSlide(); addChrome(s, 7, 'Experiment'); addTitle(s, 'Three workloads × two arms', 'The disabled arm asks the uncomfortable question: would Kubernetes have recovered anyway?');
  const rows = [
    ['W1', 'Transient self-recovery', 'Controller can make it slower', 'Unaided Kubernetes baseline'],
    ['W2', 'Bad current revision', 'rollout_undo → verify recovery', 'Broken revision remains'],
    ['W3', 'Current + previous bad', 'verification fails → restore', 'No controller action'],
  ];
  const x = [0.7, 1.85, 5.05, 8.9, 12.45];
  ['ID', 'Failure model', 'Enabled', 'Disabled'].forEach((h,i) => s.addText(h, { x: x[i], y: 1.72, w: x[i+1]-x[i]-0.08, h: 0.35, fontSize: 12, bold: true, color: C.white, fill: { color: C.navy }, margin: 0.08, align: i===0?'center':'left' }));
  rows.forEach((r,ri) => {
    const y = 2.15 + ri * 1.12;
    const fill = ri % 2 ? C.pale : C.white;
    r.forEach((v,i) => s.addText(v, { x: x[i], y, w: x[i+1]-x[i]-0.08, h: 0.82, fontSize: i===0?17:12.5, bold: i===0, color: i===0?C.teal:C.ink, fill: { color: fill }, margin: 0.1, valign: 'mid', align: i===0?'center':'left', fit: 'shrink' }));
  });
  addPill(s, 'enabled', 4.55, 6.05, 1.15, C.green, 'E3F1EA');
  s.addText('controller active', { x: 5.87, y: 6.14, w: 1.3, h: 0.2, fontSize: 10, color: C.grey, margin: 0 });
  addPill(s, 'disabled', 7.35, 6.05, 1.15, C.grey, C.pale);
  s.addText('null-action control', { x: 8.67, y: 6.14, w: 1.65, h: 0.2, fontSize: 10, color: C.grey, margin: 0 });
}

// 8 — Results
{
  const s = pptx.addSlide(); addChrome(s, 8, 'Results'); addTitle(s, 'Results are evidence-gated', 'This slide must be regenerated from archived runs; missing data is shown, never guessed.');
  const metrics = [
    ['Rollback rate / attempts', 'MISSING'],
    ['Rollback rate / incidents', 'MISSING'],
    ['Recovery by workload + arm', 'MISSING'],
    ['Attributable recovery', 'MISSING'],
    ['TTD / classify / apply / verify', 'MISSING'],
    ['Five terminal outcomes', 'MISSING'],
  ];
  metrics.forEach((m,i) => {
    const col = i % 2; const row = Math.floor(i/2);
    const x = 0.8 + col*6.05; const y=1.7+row*1.35;
    addBox(s,m[0],m[1],x,y,5.55,1.0,C.orange,C.paleOrange);
  });
  s.addText('Gate: populate only when runs/ + meta.json exist and results.py reproduces the number.', { x: 1.1, y: 6.15, w: 11.1, h: 0.45, fontSize: 16, bold: true, color: C.red, align: 'center', margin: 0 });
}

// 9 — Comparison
{
  const s = pptx.addSlide(); addChrome(s, 9, 'Positioning'); addTitle(s, 'Comparison without overclaiming', 'The final slide wording must cite the exact source passages for both comparator systems.');
  addBox(s, 'Wiesinger', 'Iterative remediation / retry behavior.\n\nDo not say “no recovery mechanism.”', 0.8, 1.75, 3.65, 3.85, C.grey, C.pale);
  addBox(s, 'ARBITER', 'Includes a safety monitor and approval-oriented experiments.\n\nDo not claim priority or absence of rollback.', 4.85, 1.75, 3.65, 3.85, C.orange, C.paleOrange);
  addBox(s, 'SAGE-K8s', 'Ungated execution for one fault class, reversible two-action allowlist, disabled control arm, and direct rollback-rate measurement.', 8.9, 1.75, 3.65, 3.85, C.teal, C.paleTeal);
  s.addText('Difference to defend: measured safety behavior under a narrow autonomous scope.', { x: 1.2, y: 6.15, w: 10.9, h: 0.4, fontSize: 19, bold: true, color: C.navy, align: 'center', margin: 0 });
}

// 10 — Limitations
{
  const s = pptx.addSlide(); addChrome(s, 10, 'Limitations'); addTitle(s, 'What this experiment does—and does not—establish', 'Naming limitations is part of the contribution, not an apology.');
  addBullets(s, [
    'A 60-second stability window creates a hard floor in time-to-mitigation.',
    'The 30/60-second constants were chosen a priori and were not sensitivity-tested.',
    'Small N and one K3s cluster limit external validity.',
    'Two actions and one failure class prioritize reversibility over breadth.',
    'PVC persistence survives controller Pod restarts, not node or disk loss.',
    'Evidence freezing preserves attribution but can miss causes visible only after an action.',
  ], 0.85, 1.55, 7.35, 4.9, { fontSize: 17, space: 10 });
  addBox(s, 'Next, after submission', 'Window-sensitivity study\nMulti-cluster replication\nBroader reversible actions\nLarger run matrix', 9.0, 1.75, 3.2, 3.45, C.teal, C.paleTeal);
  s.addText('No new features this week. Prove, archive, explain.', { x: 8.65, y: 5.65, w: 3.9, h: 0.7, fontSize: 20, bold: true, color: C.orange, align: 'center', margin: 0 });
}

pptx.writeFile({ fileName: 'docs/final-week/SAGE-K8s_Demo_Deck.pptx' });

