import { useEffect, useRef } from "react";

import {
  SPIDER,
  advanceMotion,
  createMotion,
  resetMotion,
  createWeb,
  edgesNear,
  findWebContact,
  sampleEdge,
  sampleWaves,
  type FootImpulse,
  type Point,
  type WebContact,
  type WebEdge,
  type WebSample,
} from "./spider-web.ts";

type Leg = {
  side: number;
  index: number;
  foot: Point;
  from: Point;
  to: WebContact;
  contact: WebContact;
  progress: number;
};

const TAU = Math.PI * 2;

/** Transform an anatomical offset into page coordinates. */
function offset(origin: Point, angle: number, x: number, y: number): Point {
  return {
    x: origin.x + Math.cos(angle) * x - Math.sin(angle) * y,
    y: origin.y + Math.sin(angle) * x + Math.cos(angle) * y,
  };
}

/** Find the relaxed contact position of one of the eight feet. */
function restingFoot(position: Point, angle: number, size: number, side: number, index: number): Point {
  const forward = [2.45, 1.15, -1.05, -2.4][index] ?? 0;
  const lateral = [2.25, 3.05, 3.0, 2.2][index] ?? 0;

  return offset(
    position,
    angle,
    size * forward * SPIDER.leg.stanceScale,
    size * lateral * side * SPIDER.leg.stanceScale
  );
}

/** Locate a leg's attachment to the cephalothorax. */
function legRoot(position: Point, angle: number, size: number, leg: Pick<Leg, "index" | "side">): Point {
  return offset(position, angle, (0.22 - leg.index * 0.14) * size, leg.side * size * 0.17);
}

/** Resolve the same anatomical contact for drawing and footfall excitation. */
function solveLeg(position: Point, angle: number, size: number, leg: Leg, foot: Point): Point[] {
  const root = legRoot(position, angle, size, leg);
  const dx = foot.x - root.x;
  const dy = foot.y - root.y;
  const reach = Math.max(0.01, Math.hypot(dx, dy));
  const ux = dx / reach;
  const uy = dy / reach;
  const segment = size * SPIDER.leg.segment;
  const tarsus = size * SPIDER.leg.tarsus;
  const contactReach = Math.max(tarsus, Math.min(reach, size * SPIDER.leg.drawReach));
  const distance = contactReach - tarsus;
  const bend = Math.sqrt(Math.max(0, segment * segment - (distance * distance) / 4));
  const outward = (-uy * -Math.sin(angle) + ux * Math.cos(angle)) * leg.side >= 0 ? 1 : -1;

  return [
    root,
    {
      x: root.x + (ux * distance) / 2 - uy * bend * outward,
      y: root.y + (uy * distance) / 2 + ux * bend * outward,
    },
    {
      x: root.x + ux * distance,
      y: root.y + uy * distance,
    },
    {
      x: root.x + ux * contactReach,
      y: root.y + uy * contactReach,
    },
  ];
}

/** Solve each joint from planted feet, keeping each leg six body lengths long. */
function drawSpider(
  ctx: CanvasRenderingContext2D,
  position: Point,
  angle: number,
  size: number,
  legs: Leg[],
  contacts: Point[],
  ink: string
) {
  ctx.strokeStyle = ink;
  ctx.fillStyle = ink;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";

  // Draw 8 jointed legs
  for (const [index, leg] of legs.entries()) {
    const foot = contacts[index] ?? leg.foot;
    const joints = solveLeg(position, angle, size, leg, foot);
    ctx.globalAlpha = leg.progress < 1 ? 0.74 : 0.88;

    for (let segmentIndex = 0; segmentIndex < 3; segmentIndex++) {
      const a = joints[segmentIndex];
      const b = joints[segmentIndex + 1];

      if (!a || !b) {
        continue;
      }

      ctx.lineWidth = size * (0.075 - segmentIndex * 0.023);
      ctx.beginPath();
      ctx.moveTo(a.x, a.y);
      ctx.lineTo(b.x, b.y);
      ctx.stroke();
    }
  }

  // Draw cephalothorax and abdomen
  ctx.globalAlpha = 0.94;
  ctx.save();
  ctx.translate(position.x, position.y);
  ctx.rotate(angle);
  ctx.beginPath();
  ctx.ellipse(-size * 0.19, 0, size * 0.32, size * 0.235, 0, 0, TAU);
  ctx.fill();
  ctx.beginPath();
  ctx.ellipse(size * 0.25, 0, size * 0.21, size * 0.17, 0, 0, TAU);
  ctx.fill();
  ctx.lineWidth = size * 0.045;

  for (const side of [-1, 1]) {
    ctx.beginPath();
    ctx.moveTo(size * 0.4, size * side * 0.08);
    ctx.quadraticCurveTo(size * 0.65, size * side * 0.2, size * 0.64, size * side * 0.06);
    ctx.stroke();
  }

  ctx.restore();
}

/** Animate foot-driven vibrations on a fixed viewport web background with responsive spider interaction. */
export function SpiderWebCanvas() {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const element = canvasRef.current;
    const landing = document.querySelector<HTMLElement>("[data-spider-landing]") ?? document.body;
    const context = element?.getContext("2d");

    if (!element || !context) {
      return;
    }

    const canvas = element;
    const root = landing;
    const ctx = context;
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

    let width = window.innerWidth;
    let height = window.innerHeight;
    let size = 13;
    let ink = "#35372b";
    let silk = "#687142";
    let frame = 0;
    let previous = 0;
    let clock = 0;
    let travelled = 0;
    let speed = 0;
    let angle = -0.45;
    let mesh = createWeb(width, height, size);
    let ambientCanvas: HTMLCanvasElement | null = null;

    const motion = createMotion(
      {
        x: width * 0.15,
        y: Math.min(height * 0.45, 360),
      },
      angle
    );
    const position = motion.position;
    let target = {
      x: position.x,
      y: position.y,
    };
    const impulses: FootImpulse[] = [];
    const fieldCache = new Map<string, WebSample>();
    let legs: Leg[] = [];
    let supports: Leg[] = [];
    let parkedEdges: WebEdge[] | null = null;

    /** Keep material contacts fixed until each supporting foot lifts. */
    function plantFeet() {
      legs = [-1, 1].flatMap((side) => {
        return Array.from({ length: 4 }, (_, index) => {
          const identity = {
            side,
            index,
          };
          const desired = restingFoot(position, angle, size, side, index);
          const contact = findWebContact(
            mesh,
            desired,
            legRoot(position, angle, size, identity),
            size * SPIDER.leg.contactReach
          );

          return {
            side,
            index,
            foot: contact.point,
            from: contact.point,
            to: contact,
            contact,
            progress: 1,
          };
        });
      });

      supports = legs;
      parkedEdges = null;
      fieldCache.clear();
    }

    /** Pre-render the full viewport ambient web to an offscreen canvas. */
    function renderAmbientWeb() {
      if (!ambientCanvas) {
        ambientCanvas = document.createElement("canvas");
      }

      const ratio = Math.min(window.devicePixelRatio || 1, 2);
      ambientCanvas.width = Math.round(width * ratio);
      ambientCanvas.height = Math.round(height * ratio);
      const aCtx = ambientCanvas.getContext("2d");

      if (!aCtx) {
        return;
      }

      aCtx.setTransform(ratio, 0, 0, ratio, 0, 0);
      aCtx.clearRect(0, 0, width, height);
      aCtx.strokeStyle = silk;
      aCtx.lineWidth = 0.55;
      aCtx.globalAlpha = 0.065;
      aCtx.beginPath();

      // Render all viewport-visible threads
      for (const edge of mesh.edges) {
        if (
          Math.max(edge.a.x, edge.b.x) < -size ||
          Math.min(edge.a.x, edge.b.x) > width + size ||
          Math.max(edge.a.y, edge.b.y) < -size ||
          Math.min(edge.a.y, edge.b.y) > height + size
        ) {
          continue;
        }

        aCtx.moveTo(edge.a.x, edge.a.y);
        const mid = sampleEdge(edge, 0.5);
        aCtx.quadraticCurveTo(mid.x, mid.y, edge.b.x, edge.b.y);
      }

      aCtx.stroke();
    }

    /** Schedule visible work without resetting an already active clock. */
    function wake() {
      if (!frame && !document.hidden) {
        previous = performance.now();
        frame = requestAnimationFrame(paint);
      }
    }

    /** Match body and canvas resolution to current viewport dimensions. */
    function resize() {
      width = window.innerWidth;
      height = window.innerHeight;
      const ratio = Math.min(window.devicePixelRatio || 1, 2);

      canvas.width = Math.round(width * ratio);
      canvas.height = Math.round(height * ratio);
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
      ctx.setTransform(ratio, 0, 0, ratio, 0, 0);

      const paragraph = root.querySelector("#pipeline .landing-spotlight p");
      size = paragraph ? parseFloat(getComputedStyle(paragraph).fontSize) || 13 : 13;

      // Rebuild viewport mesh
      mesh = createWeb(width, height, size);

      const margin = Math.min(size * SPIDER.leg.length, width / 4);
      position.x = Math.max(margin, Math.min(width - margin, position.x));
      position.y = Math.max(margin, Math.min(height - margin, position.y));

      target.x = Math.max(margin, Math.min(width - margin, target.x));
      target.y = Math.max(margin, Math.min(height - margin, target.y));

      impulses.length = 0;
      resetMotion(motion);
      speed = 0;
      plantFeet();
      readTheme();
      wake();
    }

    /** Resolve silk and body colors when the theme changes. */
    function readTheme() {
      ink = getComputedStyle(root).color;
      silk = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() || ink;

      renderAmbientWeb();
      wake();
    }

    /** Follow mouse or pen movement strictly within viewport coordinates. */
    function followPointer(event: PointerEvent) {
      if (!event.isPrimary || event.pointerType === "touch") {
        return;
      }

      // Keep target firmly within viewport margins
      target = {
        x: Math.max(size * 2, Math.min(width - size * 2, event.clientX)),
        y: Math.max(size * 2, Math.min(height - size * 2, event.clientY)),
      };

      if (reducedMotion.matches) {
        position.x = target.x;
        position.y = target.y;
        resetMotion(motion);
        speed = 0;
        impulses.length = 0;
        plantFeet();
      }

      wake();
    }

    /** Combine coherent impact waves and very small supporting-foot tensions. */
    function fieldAt(point: Point): WebSample {
      const key = `${point.x}:${point.y}`;
      const cached = fieldCache.get(key);

      if (cached) {
        return cached;
      }

      const wave = sampleWaves(point, impulses, clock, size);
      let displacement = wave.displacement;

      if (!reducedMotion.matches) {
        for (const leg of supports) {
          const distance = Math.hypot(point.x - leg.contact.point.x, point.y - leg.contact.point.y);
          const weight = Math.max(0, 1 - distance / (size * SPIDER.web.supportRadius));
          displacement += Math.sin(clock * 0.005 + leg.index * 1.3 + leg.side * 0.7) * size * 0.025 * weight * weight;
        }
      }

      const value = {
        displacement: Math.max(-size * SPIDER.web.displacement, Math.min(size * SPIDER.web.displacement, displacement)),
        strength: wave.strength,
      };

      fieldCache.set(key, value);
      return value;
    }

    /** Deform only thread interiors; its original endpoints remain anchored. */
    function materialPoint(edge: WebEdge, t: number): Point {
      const point = sampleEdge(edge, t);
      const displacement = fieldAt(point).displacement * Math.sin(t * Math.PI);

      return {
        x: point.x + displacement * 0.35,
        y: point.y + displacement * 0.94,
      };
    }

    /** Calculate a leg's actual displayed foot on the same curve as the visible silk. */
    function footPosition(leg: Leg): Point {
      return leg.progress < 1 ? leg.foot : materialPoint(leg.contact.edge, leg.contact.t);
    }

    /** Plan a reachable landing ahead of the moving body. */
    function startStep(leg: Leg, effectiveSpeed: number) {
      const duration = Math.max(
        SPIDER.leg.swingMin,
        Math.min(SPIDER.leg.swingMax, (size * SPIDER.leg.swingDistance) / Math.max(effectiveSpeed, 1))
      );
      const landingPosition = {
        x: position.x + motion.velocity.x * duration * 0.75,
        y: position.y + motion.velocity.y * duration * 0.75,
      };
      const landingAngle = angle + motion.angularVelocity * duration * 0.75;
      const desired = restingFoot(landingPosition, landingAngle, size, leg.side, leg.index);

      leg.from = footPosition(leg);
      leg.to = findWebContact(
        mesh,
        desired,
        legRoot(landingPosition, landingAngle, size, leg),
        size * SPIDER.leg.contactReach
      );
      leg.progress = 0;
      parkedEdges = null;
    }

    /** Emit exactly once per touchdown, using the rendered anatomical endpoint. */
    function exciteFoot(leg: Leg) {
      if (reducedMotion.matches) {
        return;
      }

      const joints = solveLeg(position, angle, size, leg, footPosition(leg));
      const contact = joints[3];

      if (!contact) {
        return;
      }

      impulses.push({
        x: contact.x,
        y: contact.y,
        time: clock,
        amplitude: size * 0.24,
        phase: 0,
      });

      if (impulses.length > SPIDER.web.maxImpulses) {
        impulses.shift();
      }
    }

    /** Alternate planted quartets and keep each completed step on a reachable thread. */
    function moveLegs(dt: number) {
      const landed: Leg[] = [];
      const angularSpeed = Math.abs(motion.angularVelocity);
      const effectiveSpeed = Math.max(speed, angularSpeed * size * 1.8);
      const moving = !motion.settled && (effectiveSpeed > size * SPIDER.motion.stopSpeed || angularSpeed > 0.12);

      for (const leg of legs) {
        const desired = restingFoot(position, angle, size, leg.side, leg.index);
        const rootPoint = legRoot(position, angle, size, leg);
        const stretched = Math.hypot(desired.x - leg.contact.point.x, desired.y - leg.contact.point.y);
        const reach = Math.hypot(rootPoint.x - leg.contact.point.x, rootPoint.y - leg.contact.point.y);
        const phase = ((travelled / (size * SPIDER.leg.stride)) + (((leg.index + (leg.side > 0 ? 1 : 0)) % 2) * 0.5)) % 1;

        if (
          leg.progress >= 1 &&
          (reach > size * SPIDER.leg.contactReach ||
            (moving &&
              stretched > size * SPIDER.leg.stepThreshold &&
              (phase < 0.45 || stretched > size * SPIDER.leg.urgentStep)))
        ) {
          startStep(leg, effectiveSpeed);
        }

        if (leg.progress >= 1) {
          continue;
        }

        const duration = moving
          ? Math.max(
              SPIDER.leg.swingMin,
              Math.min(SPIDER.leg.swingMax, (size * SPIDER.leg.swingDistance) / Math.max(effectiveSpeed, 1))
            )
          : 0.18;

        leg.progress = Math.min(1, leg.progress + dt / duration);
        const t = leg.progress;
        const smooth = t * t * (3 - 2 * t);
        const lift = Math.sin(t * Math.PI) * size * SPIDER.leg.lift;

        leg.foot = {
          x: leg.from.x + (leg.to.point.x - leg.from.x) * smooth - Math.sin(angle) * lift * leg.side,
          y: leg.from.y + (leg.to.point.y - leg.from.y) * smooth + Math.cos(angle) * lift * leg.side,
        };

        if (t === 1) {
          leg.contact = findWebContact(mesh, leg.to.point, rootPoint, size * SPIDER.leg.contactReach);
          leg.foot = leg.contact.point;
          landed.push(leg);
        }
      }

      supports = legs.filter((leg) => {
        return leg.progress >= 1;
      });
      fieldCache.clear();

      for (const leg of landed) {
        exciteFoot(leg);
      }
    }

    /** Advance the body with bounded acceleration, braking and smooth heading. */
    function move(dt: number) {
      if (!reducedMotion.matches) {
        travelled += advanceMotion(motion, target, size, dt);
      }

      speed = Math.hypot(motion.velocity.x, motion.velocity.y);
      angle = motion.heading;
      moveLegs(dt);
    }

    /** Reveal only active vibrating and loaded threads with dynamic silk curvature. */
    function drawActiveWeb() {
      const points = supports.map((leg) => {
        return leg.contact.point;
      });

      if (!parkedEdges && motion.settled && supports.length === 8) {
        parkedEdges = edgesNear(mesh, points, size * SPIDER.web.supportRadius);
      }

      const candidates = new Map<string, WebEdge>();

      for (const edge of parkedEdges ?? edgesNear(mesh, points, size * SPIDER.web.supportRadius)) {
        candidates.set(edge.id, edge);
      }

      for (const edge of edgesNear(mesh, impulses, size * SPIDER.web.waveRadius)) {
        candidates.set(edge.id, edge);
      }

      ctx.strokeStyle = silk;

      for (const edge of candidates.values()) {
        if (
          Math.max(edge.a.y, edge.b.y) < -size ||
          Math.min(edge.a.y, edge.b.y) > height + size ||
          Math.max(edge.a.x, edge.b.x) < -size ||
          Math.min(edge.a.x, edge.b.x) > width + size
        ) {
          continue;
        }

        const steps = Math.max(4, Math.ceil(edge.length / (size * 0.65)));
        const vertices: Point[] = [];
        let support = 0;
        let strength = 0;

        for (let step = 0; step <= steps; step++) {
          const t = step / steps;
          const point = sampleEdge(edge, t);

          for (const leg of supports) {
            const distance = Math.hypot(point.x - leg.contact.point.x, point.y - leg.contact.point.y);
            support = Math.max(support, Math.max(0, 1 - distance / (size * SPIDER.web.supportRadius)));
          }

          strength = Math.max(strength, fieldAt(point).strength);
          vertices.push(materialPoint(edge, t));
        }

        ctx.globalAlpha = Math.min(0.7, support * 0.22 + strength * 0.45);

        if (ctx.globalAlpha < 0.005) {
          continue;
        }

        ctx.lineWidth = 0.75 + strength * 0.35;
        ctx.beginPath();

        for (const [index, point] of vertices.entries()) {
          if (index === 0) {
            ctx.moveTo(point.x, point.y);
          } else {
            ctx.lineTo(point.x, point.y);
          }
        }

        ctx.stroke();
      }
    }

    /** Render active waves, web tension, and spider movement cleanly within the viewport. */
    function paint(now: number) {
      frame = 0;

      if (reducedMotion.matches && (impulses.length > 0 || supports.length !== 8 || !motion.settled)) {
        impulses.length = 0;
        resetMotion(motion);
        speed = 0;
        plantFeet();
      }

      const dt = previous ? Math.min((now - previous) / 1000, 0.05) : 1 / 60;
      previous = now;
      clock += dt * 1000;
      fieldCache.clear();

      // Advance physics simulation
      move(dt);

      while (impulses[0] && clock - impulses[0].time >= SPIDER.web.waveLifetime) {
        impulses.shift();
      }

      // Clear viewport background
      ctx.clearRect(0, 0, width, height);

      // 1. Draw static ambient full-screen web background
      if (ambientCanvas) {
        ctx.drawImage(ambientCanvas, 0, 0);
      }

      // 2. Draw active deformed web threads
      drawActiveWeb();

      // 3. Draw spider body and legs
      const contacts = legs.map(footPosition);
      drawSpider(ctx, position, angle, size, legs, contacts, ink);

      // Schedule next frame if not fully settled
      const fullySettled = motion.settled && impulses.length === 0 && supports.length === 8;

      if (!reducedMotion.matches && !document.hidden && !fullySettled) {
        frame = requestAnimationFrame(paint);
      }
    }

    /** Freeze time while the tab is hidden. */
    function visibility() {
      cancelAnimationFrame(frame);
      frame = 0;
      previous = 0;
      wake();
    }

    const themeObserver = new MutationObserver(readTheme);
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });

    resize();
    window.addEventListener("pointermove", followPointer, { passive: true });
    window.addEventListener("resize", resize);
    document.addEventListener("visibilitychange", visibility);
    reducedMotion.addEventListener("change", resize);

    return () => {
      cancelAnimationFrame(frame);
      themeObserver.disconnect();
      window.removeEventListener("pointermove", followPointer);
      window.removeEventListener("resize", resize);
      document.removeEventListener("visibilitychange", visibility);
      reducedMotion.removeEventListener("change", resize);
    };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      data-spider-canvas
      aria-hidden="true"
      className="pointer-events-none fixed inset-0 z-0"
      style={{
        position: "fixed",
        top: 0,
        left: 0,
        width: "100vw",
        height: "100vh",
        zIndex: 0,
        pointerEvents: "none",
      }}
    />
  );
}
