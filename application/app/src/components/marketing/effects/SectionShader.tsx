import { type Component, onCleanup, onMount } from 'solid-js';

const VERT = `#version 300 es
precision mediump float;
in vec2 aPos;
out vec2 vUv;
void main() {
  vUv = aPos * 0.5 + 0.5;
  gl_Position = vec4(aPos, 0.0, 1.0);
}`;

const FRAG = `#version 300 es
precision mediump float;
in vec2 vUv;
out vec4 fragColor;

uniform float uTime;
uniform float uScroll;
uniform vec3 uColorA;
uniform vec3 uColorB;

float hash(vec2 p) {
  return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453123);
}

float noise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  f = f * f * (3.0 - 2.0 * f);

  float a = hash(i);
  float b = hash(i + vec2(1.0, 0.0));
  float c = hash(i + vec2(0.0, 1.0));
  float d = hash(i + vec2(1.0, 1.0));

  return mix(mix(a, b, f.x), mix(c, d, f.x), f.y);
}

void main() {
  vec2 uv = vUv;
  vec2 drift = vec2(uTime * 0.035, -uTime * 0.02);

  float field = noise(uv * 4.0 + drift);
  field += noise(uv * 9.0 - drift * 1.6) * 0.45;
  field *= 0.7;

  float beam = smoothstep(0.42, 0.0, abs(uv.y - 0.5));
  beam *= 0.26 + 0.18 * sin((uv.x + uTime * 0.03) * 8.0);

  float ring = 1.0 - smoothstep(0.08, 0.42, length(uv - vec2(0.5)));
  ring *= 0.12;

  float mixFactor = clamp(field + uScroll * 0.28 + beam * 0.18, 0.0, 1.0);
  vec3 color = mix(uColorA, uColorB, mixFactor);

  float fadeX = smoothstep(0.0, 0.18, uv.x) * smoothstep(1.0, 0.82, uv.x);
  float fadeY = smoothstep(0.0, 0.25, uv.y) * smoothstep(1.0, 0.75, uv.y);
  float alpha = (beam + field * 0.16 + ring) * fadeX * fadeY;

  fragColor = vec4(color, alpha);
}`;

export interface SectionShaderProps {
  class?: string;
  colorA?: [number, number, number];
  colorB?: [number, number, number];
}

const SectionShader: Component<SectionShaderProps> = (props) => {
  let canvasRef!: HTMLCanvasElement;

  onMount(() => {
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const gl = canvasRef.getContext('webgl2', { alpha: true, premultipliedAlpha: true });
    if (!gl) return;

    let raf = 0;
    let visible = true;
    let tabVisible = !document.hidden;

    const compile = (type: number, source: string) => {
      const shader = gl.createShader(type);
      if (!shader) return null;
      gl.shaderSource(shader, source);
      gl.compileShader(shader);
      return shader;
    };

    const vertexShader = compile(gl.VERTEX_SHADER, VERT);
    const fragmentShader = compile(gl.FRAGMENT_SHADER, FRAG);
    if (!vertexShader || !fragmentShader) return;

    const program = gl.createProgram();
    if (!program) return;

    gl.attachShader(program, vertexShader);
    gl.attachShader(program, fragmentShader);
    gl.linkProgram(program);
    gl.useProgram(program);

    const vertices = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, vertices);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);

    const position = gl.getAttribLocation(program, 'aPos');
    gl.enableVertexAttribArray(position);
    gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);

    const uTime = gl.getUniformLocation(program, 'uTime');
    const uScroll = gl.getUniformLocation(program, 'uScroll');
    const uColorA = gl.getUniformLocation(program, 'uColorA');
    const uColorB = gl.getUniformLocation(program, 'uColorB');

    gl.uniform3fv(uColorA, props.colorA || [0.1, 0.45, 0.7]);
    gl.uniform3fv(uColorB, props.colorB || [0.46, 0.86, 1.0]);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

    const resize = () => {
      const rect = canvasRef.getBoundingClientRect();
      const isMobile = window.innerWidth < 768;
      const dpr = Math.min(window.devicePixelRatio, isMobile ? 1.0 : 1.5);
      canvasRef.width = Math.max(1, Math.floor(rect.width * dpr));
      canvasRef.height = Math.max(1, Math.floor(rect.height * dpr));
      gl.viewport(0, 0, canvasRef.width, canvasRef.height);
    };

    const handleVisibility = () => {
      tabVisible = !document.hidden;
    };

    const intersectionObserver = new IntersectionObserver(
      ([entry]) => {
        visible = entry?.isIntersecting ?? true;
      },
      { threshold: 0.05 },
    );
    intersectionObserver.observe(canvasRef);

    const resizeObserver = new ResizeObserver(resize);
    resizeObserver.observe(canvasRef);

    const loop = () => {
      raf = requestAnimationFrame(loop);
      if (!visible || !tabVisible) return;

      const elapsed = performance.now() * 0.001;
      const scrollHeight = Math.max(document.documentElement.scrollHeight - window.innerHeight, 1);
      const scroll = window.scrollY / scrollHeight;

      gl.uniform1f(uTime, reducedMotion ? 0.0 : elapsed);
      gl.uniform1f(uScroll, scroll);
      gl.clearColor(0, 0, 0, 0);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    };

    resize();
    document.addEventListener('visibilitychange', handleVisibility);
    loop();

    onCleanup(() => {
      cancelAnimationFrame(raf);
      document.removeEventListener('visibilitychange', handleVisibility);
      intersectionObserver.disconnect();
      resizeObserver.disconnect();
      if (vertices) gl.deleteBuffer(vertices);
      gl.deleteShader(vertexShader);
      gl.deleteShader(fragmentShader);
      gl.deleteProgram(program);
    });
  });

  return (
    <div class={`v3-section-shader ${props.class || ''}`} aria-hidden="true">
      <canvas ref={canvasRef} />
    </div>
  );
};

export default SectionShader;
