export type Point = {
  x: number;
  y: number;
};

export type WebEdge = {
  id: string;
  a: Point;
  b: Point;
  length: number;
  sag: number;
};

export type WebMesh = {
  edges: WebEdge[];
  spacing: number;
  cells: Map<string, WebEdge[]>;
};

export type WebContact = {
  edge: WebEdge;
  t: number;
  point: Point;
};

export type FootImpulse = Point & {
  time: number;
  amplitude: number;
  phase: number;
};

export type WebSample = {
  displacement: number;
  strength: number;
};

export const SPIDER = {
  leg: {
    length: 6,
    segment: 2.76,
    tarsus: 0.48,
    drawReach: 5.995,
    contactReach: 5.25,
    stanceScale: 1.5,
    stride: 4.8,
    stepThreshold: 0.975,
    urgentStep: 2.55,
    swingDistance: 2.55,
    swingMin: 0.08,
    swingMax: 0.21,
    lift: 0.27,
  },
  web: {
    supportRadius: 4.5,
    waveRadius: 12,
    waveLifetime: 2200,
    maxImpulses: 64,
    displacement: 0.45,
  },
  motion: {
    maxSpeed: 28,
    acceleration: 70,
    braking: 56,
    jerk: 560,
    stiffness: 22,
    damping: 10,
    angularSpeed: 8.0,
    angularAcceleration: 24,
    stopDistance: 0.08,
    stopSpeed: 0.12,
    stopAcceleration: 0.5,
    wakeDistance: 0.2,
    step: 1 / 120,
  },
} as const;

export type MotionState = {
  position: Point;
  velocity: Point;
  acceleration: Point;
  heading: number;
  angularVelocity: number;
  settled: boolean;
  remainder: number;
};

const TAU = Math.PI * 2;

/** Initialize resting motion while sharing the body's page-space position. */
export function createMotion(position: Point, heading: number): MotionState {
  return {
    position,
    velocity: {
      x: 0,
      y: 0,
    },
    acceleration: {
      x: 0,
      y: 0,
    },
    heading,
    angularVelocity: 0,
    settled: true,
    remainder: 0,
  };
}

/** Clear inertia after a deliberate reposition or a fully settled arrival. */
export function resetMotion(state: MotionState): void {
  state.velocity.x = 0;
  state.velocity.y = 0;
  state.acceleration.x = 0;
  state.acceleration.y = 0;
  state.angularVelocity = 0;
  state.remainder = 0;
  state.settled = true;
}

/** Limit a vector without changing its direction. */
function limitVector(vector: Point, limit: number): Point {
  const length = Math.hypot(vector.x, vector.y);
  const scale = length > limit ? limit / length : 1;
  return {
    x: vector.x * scale,
    y: vector.y * scale,
  };
}

/** Check cruise speed together with the room needed to unload acceleration continuously. */
function withinSpeedEnvelope(velocity: Point, current: Point, next: Point, dt: number, maxSpeed: number, jerk: number): boolean {
  const vx = velocity.x + (current.x + next.x) * dt / 2;
  const vy = velocity.y + (current.y + next.y) * dt / 2;
  const unload = Math.hypot(next.x, next.y) / (2 * jerk) + dt / 2;
  const tolerance = maxSpeed + 1e-9;
  return Math.hypot(vx, vy) <= tolerance && Math.hypot(vx + next.x * unload, vy + next.y * unload) <= tolerance;
}

/** Approach the requested force within the jerk bound and the cruise-speed envelope. */
function nextAcceleration(state: MotionState, target: Point, size: number, dt: number): Point {
  const cfg = SPIDER.motion;
  const toX = target.x - state.position.x;
  const toY = target.y - state.position.y;
  const dist = Math.hypot(toX, toY);

  // Orient driving force smoothly with current heading to avoid sliding sideways or backwards
  let desired = {
    x: toX * cfg.stiffness - state.velocity.x * cfg.damping,
    y: toY * cfg.stiffness - state.velocity.y * cfg.damping,
  };

  if (dist > size * 0.5) {
    const desiredAngle = Math.atan2(desired.y, desired.x);
    const angleDiff = Math.abs(Math.atan2(Math.sin(desiredAngle - state.heading), Math.cos(desiredAngle - state.heading)));
    const alignment = Math.max(0.35, Math.cos(angleDiff));

    desired = {
      x: desired.x * alignment,
      y: desired.y * alignment,
    };
  }

  const braking = desired.x * state.velocity.x + desired.y * state.velocity.y < 0;
  const force = limitVector(desired, size * (braking ? cfg.braking : cfg.acceleration));
  const jerk = size * cfg.jerk;
  const change = limitVector({
    x: force.x - state.acceleration.x,
    y: force.y - state.acceleration.y,
  }, jerk * dt);
  const candidate = {
    x: state.acceleration.x + change.x,
    y: state.acceleration.y + change.y,
  };
  const maxSpeed = size * cfg.maxSpeed;

  if (withinSpeedEnvelope(state.velocity, state.acceleration, candidate, dt, maxSpeed, jerk)) {
    return candidate;
  }

  // Braking the existing acceleration to zero is always reachable without a force discontinuity.
  const magnitude = Math.hypot(state.acceleration.x, state.acceleration.y);
  const unload = magnitude > 0 ? Math.max(0, 1 - jerk * dt / magnitude) : 0;
  const safe = {
    x: state.acceleration.x * unload,
    y: state.acceleration.y * unload,
  };
  let lower = 0;
  let upper = 1;

  for (let iteration = 0; iteration < 18; iteration++) {
    const blend = (lower + upper) / 2;
    const trial = {
      x: safe.x + (candidate.x - safe.x) * blend,
      y: safe.y + (candidate.y - safe.y) * blend,
    };

    if (withinSpeedEnvelope(state.velocity, state.acceleration, trial, dt, maxSpeed, jerk)) {
      lower = blend;
    } else {
      upper = blend;
    }
  }

  return {
    x: safe.x + (candidate.x - safe.x) * lower,
    y: safe.y + (candidate.y - safe.y) * lower,
  };
}

/** Turn smoothly toward target and travel direction with natural damping and responsiveness. */
function advanceHeading(state: MotionState, target: Point, size: number, dt: number): void {
  const dx = target.x - state.position.x;
  const dy = target.y - state.position.y;
  const dist = Math.hypot(dx, dy);
  const speed = Math.hypot(state.velocity.x, state.velocity.y);

  // Determine desired heading based on movement and destination
  let desiredHeading = state.heading;

  if (dist > size * 0.4) {
    const toTarget = Math.atan2(dy, dx);

    if (speed > size * 1.2) {
      const velHeading = Math.atan2(state.velocity.y, state.velocity.x);
      const diff = Math.atan2(Math.sin(toTarget - velHeading), Math.cos(toTarget - velHeading));
      desiredHeading = velHeading + diff * 0.4;
    } else {
      desiredHeading = toTarget;
    }
  }

  const error = Math.atan2(Math.sin(desiredHeading - state.heading), Math.cos(desiredHeading - state.heading));

  // Organic exponential turn smoothing: snappy, smooth, and free of oscillation
  const turnRate = 11.0;
  const turnAmount = error * (1 - Math.exp(-turnRate * dt));

  state.heading = Math.atan2(Math.sin(state.heading + turnAmount), Math.cos(state.heading + turnAmount));
  state.angularVelocity = dt > 0 ? turnAmount / dt : 0;
}

/** Integrate one bounded physical step and recognize a quiet, non-oscillating arrival. */
function motionStep(state: MotionState, target: Point, size: number): number {
  const cfg = SPIDER.motion;
  const dt = cfg.step;
  const prevHeading = state.heading;

  // Turn toward target and travel direction smoothly
  advanceHeading(state, target, size, dt);

  // Compute linear physics step
  const next = nextAcceleration(state, target, size, dt);
  const velocity = {
    x: state.velocity.x + (state.acceleration.x + next.x) * dt / 2,
    y: state.velocity.y + (state.acceleration.y + next.y) * dt / 2,
  };
  const dx = (state.velocity.x + velocity.x) * dt / 2;
  const dy = (state.velocity.y + velocity.y) * dt / 2;

  state.position.x += dx;
  state.position.y += dy;
  state.velocity = velocity;
  state.acceleration = next;

  // Calculate angular delta to advance gait phase during turning
  const dHeading = Math.abs(Math.atan2(Math.sin(state.heading - prevHeading), Math.cos(state.heading - prevHeading)));

  // Settle when position, velocity, and turning are all quiet
  if (
    Math.hypot(target.x - state.position.x, target.y - state.position.y) < size * cfg.stopDistance &&
    Math.hypot(velocity.x, velocity.y) < size * cfg.stopSpeed &&
    Math.hypot(next.x, next.y) < size * cfg.stopAcceleration &&
    dHeading < 0.005
  ) {
    resetMotion(state);
  }

  // Combined linear and rotational distance for natural leg stepping
  return Math.hypot(dx, dy) + size * 2.2 * dHeading;
}

/** Advance inertial motion at a fixed physical rate, independent of rendering frequency. */
export function advanceMotion(state: MotionState, target: Point, size: number, dt: number): number {
  if (state.settled) {
    if (Math.hypot(target.x - state.position.x, target.y - state.position.y) <= size * SPIDER.motion.wakeDistance) {
      return 0;
    }
    state.settled = false;
  }
  state.remainder += Math.max(0, Math.min(0.05, dt));
  let distance = 0;
  while (state.remainder + 1e-12 >= SPIDER.motion.step && !state.settled) {
    state.remainder = Math.max(0, state.remainder - SPIDER.motion.step);
    distance += motionStep(state, target, size);
  }
  return distance;
}

/** Derive stable irregularity from a node's page-space cell, independent of viewport size. */
function noise(column: number, row: number, salt: number): number {
  let value = Math.imul(column, 374761393) ^ Math.imul(row, 668265263) ^ salt;
  value = Math.imul(value ^ value >>> 13, 1274126177);
  return ((value ^ value >>> 16) >>> 0) / 4294967295;
}

/** Locate a fixed material point along one slightly slack thread. */
export function sampleEdge(edge: WebEdge, t: number): Point {
  if (t <= 0) {
    return edge.a;
  }
  if (t >= 1) {
    return edge.b;
  }
  const dx = edge.b.x - edge.a.x;
  const dy = edge.b.y - edge.a.y;
  const distance = Math.hypot(dx, dy) || 1;
  const bend = 2 * t * (1 - t) * edge.sag;
  return {
    x: edge.a.x + dx * t - dy / distance * bend,
    y: edge.a.y + dy * t + dx / distance * bend,
  };
}

/** Index a thread into every spatial cell its static curve can cross. */
function indexEdge(mesh: WebMesh, edge: WebEdge) {
  const padding = Math.abs(edge.sag);
  const left = Math.floor((Math.min(edge.a.x, edge.b.x) - padding) / mesh.spacing);
  const right = Math.floor((Math.max(edge.a.x, edge.b.x) + padding) / mesh.spacing);
  const top = Math.floor((Math.min(edge.a.y, edge.b.y) - padding) / mesh.spacing);
  const bottom = Math.floor((Math.max(edge.a.y, edge.b.y) + padding) / mesh.spacing);
  for (let row = top; row <= bottom; row++) {
    for (let column = left; column <= right; column++) {
      const key = `${column}:${row}`;
      const cell = mesh.cells.get(key);
      if (cell) {
        cell.push(edge);
      } else {
        mesh.cells.set(key, [edge]);
      }
    }
  }
}

/** Build a fixed, irregular triangular web with generous off-page contact margins. */
export function createWeb(width: number, height: number, size: number): WebMesh {
  const spacing = Math.max(18, size * 2.4);
  const mesh: WebMesh = {
    edges: [],
    spacing,
    cells: new Map(),
  };
  const nodes = new Map<string, Point>();

  /** Reuse nodes so neighboring cells share the same immutable anchor coordinates. */
  function node(column: number, row: number): Point {
    const key = `${column}:${row}`;
    const existing = nodes.get(key);
    if (existing) {
      return existing;
    }
    const point = {
      x: (column + (noise(column, row, 137) - 0.5) * 0.76) * spacing,
      y: (row + (noise(column, row, 751) - 0.5) * 0.76) * spacing,
    };
    nodes.set(key, point);
    return point;
  }

  /** Save rest geometry and curved rest length once, before any foot interaction. */
  function connect(column: number, row: number, dc: number, dr: number) {
    const edge: WebEdge = {
      id: `${column}:${row}:${dc}:${dr}`,
      a: node(column, row),
      b: node(column + dc, row + dr),
      length: 0,
      sag: spacing * (0.035 + noise(column, row, 912 + dc * 31 + dr) * 0.07),
    };
    let previous = edge.a;
    for (let step = 1; step <= 8; step++) {
      const point = sampleEdge(edge, step / 8);
      edge.length += Math.hypot(point.x - previous.x, point.y - previous.y);
      previous = point;
    }
    mesh.edges.push(edge);
    indexEdge(mesh, edge);
  }

  const columns = Math.ceil(Math.max(0, width) / spacing) + 4;
  const rows = Math.ceil(Math.max(0, height) / spacing) + 4;

  for (let row = -4; row <= rows; row++) {
    for (let column = -4; column <= columns; column++) {
      connect(column, row, 1, 0);
      connect(column, row, 0, 1);

      if (noise(column, row, 257) > 0.5) {
        connect(column, row, 1, 1);
      } else {
        connect(column + 1, row, -1, 1);
      }
    }
  }

  return mesh;
}

/** Query and deduplicate only static threads near the supplied interaction centers. */
export function edgesNear(mesh: WebMesh, centres: Point[], radius: number): WebEdge[] {
  const nearby = new Map<string, WebEdge>();

  for (const center of centres) {
    const left = Math.floor((center.x - radius) / mesh.spacing);
    const right = Math.floor((center.x + radius) / mesh.spacing);
    const top = Math.floor((center.y - radius) / mesh.spacing);
    const bottom = Math.floor((center.y + radius) / mesh.spacing);

    for (let row = top; row <= bottom; row++) {
      for (let column = left; column <= right; column++) {
        for (const edge of mesh.cells.get(`${column}:${row}`) ?? []) {
          nearby.set(edge.id, edge);
        }
      }
    }
  }

  return [...nearby.values()];
}

/** Find a reachable static curve point close to one requested foot location. */
function contactOnEdge(edge: WebEdge, desired: Point, root: Point, maxReach: number): WebContact | null {
  let contact: WebContact | null = null;
  let best = Infinity;
  let previous = edge.a;

  for (let step = 0; step <= 12; step++) {
    const t = step / 12;
    const current = sampleEdge(edge, t);
    const dx = current.x - previous.x;
    const dy = current.y - previous.y;
    const lengthSquared = dx * dx + dy * dy;
    const projection =
      lengthSquared > 0
        ? Math.max(0, Math.min(1, ((desired.x - previous.x) * dx + (desired.y - previous.y) * dy) / lengthSquared))
        : 1;
    const projectedT = Math.max(0, (step - 1 + projection) / 12);

    for (const candidateT of [t, projectedT]) {
      const point = sampleEdge(edge, candidateT);

      if (Math.hypot(point.x - root.x, point.y - root.y) > maxReach) {
        continue;
      }

      const score = (point.x - desired.x) ** 2 + (point.y - desired.y) ** 2;

      if (score < best) {
        best = score;
        contact = {
          edge,
          t: candidateT,
          point,
        };
      }
    }

    previous = current;
  }

  return contact;
}

/** Project a foot onto nearby silk while respecting its anatomical reach. */
export function findWebContact(mesh: WebMesh, desired: Point, root: Point, maxReach: number): WebContact {
  let best: WebContact | null = null;
  let score = Infinity;
  const edges = edgesNear(mesh, [desired, root], maxReach + mesh.spacing);

  for (const edge of edges) {
    const contact = contactOnEdge(edge, desired, root, maxReach);

    if (!contact) {
      continue;
    }

    const distance = (contact.point.x - desired.x) ** 2 + (contact.point.y - desired.y) ** 2;

    if (distance < score) {
      best = contact;
      score = distance;
    }
  }

  if (!best) {
    const fallbackEdge = edges[0] ?? mesh.edges[0];

    if (fallbackEdge) {
      return {
        edge: fallbackEdge,
        t: 0.5,
        point: desired,
      };
    }

    throw new RangeError("No reachable spider-web thread within the page mesh");
  }

  return best;
}

/** Superpose signed, damped wave packets before deriving displacement and coherent brightness. */
export function sampleWaves(point: Point, impulses: FootImpulse[], clock: number, size: number): WebSample {
  let real = 0;
  let imaginary = 0;
  const radius = size * SPIDER.web.waveRadius;
  const lifetime = SPIDER.web.waveLifetime / 1000;
  for (const impulse of impulses) {
    const age = (clock - impulse.time) / 1000;
    if (age <= 0 || age >= lifetime) {
      continue;
    }
    const dx = point.x - impulse.x;
    const dy = point.y - impulse.y;
    const squared = dx * dx + dy * dy;
    if (squared >= radius * radius) {
      continue;
    }
    const distance = Math.sqrt(squared);
    const wavefront = age * size * 8;
    const spread = (distance - wavefront) / (size * 1.15);
    const attack = Math.sin(Math.min(1, age / 0.06) * Math.PI / 2) ** 2;
    const envelope = attack * Math.exp(-spread * spread) * Math.exp(-age * 1.2) * (1 - distance / radius) * (1 - age / lifetime);
    const phase = (distance / (size * 2.5) - age * 3.2) * TAU + impulse.phase;
    const amplitude = impulse.amplitude * envelope;
    real += Math.sin(phase) * amplitude;
    imaginary += Math.cos(phase) * amplitude;
  }
  const limit = size * SPIDER.web.displacement;
  return {
    displacement: Math.max(-limit, Math.min(limit, real)),
    strength: Math.min(1, Math.hypot(real, imaginary) / limit),
  };
}
