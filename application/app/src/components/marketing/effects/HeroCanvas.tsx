import { type Component, onCleanup, onMount } from 'solid-js';
import * as THREE from 'three';

const CORE_VERTEX = /* glsl */ `
  uniform float uTime;
  uniform float uPulse;

  varying vec3 vWorld;
  varying vec3 vNormal;
  varying float vNoise;

  float hash(vec3 p) {
    p = fract(p * 0.3183099 + vec3(0.1));
    p *= 17.0;
    return fract(p.x * p.y * p.z * (p.x + p.y + p.z));
  }

  float noise(vec3 x) {
    vec3 i = floor(x);
    vec3 f = fract(x);
    f = f * f * (3.0 - 2.0 * f);

    float n000 = hash(i + vec3(0.0, 0.0, 0.0));
    float n100 = hash(i + vec3(1.0, 0.0, 0.0));
    float n010 = hash(i + vec3(0.0, 1.0, 0.0));
    float n110 = hash(i + vec3(1.0, 1.0, 0.0));
    float n001 = hash(i + vec3(0.0, 0.0, 1.0));
    float n101 = hash(i + vec3(1.0, 0.0, 1.0));
    float n011 = hash(i + vec3(0.0, 1.0, 1.0));
    float n111 = hash(i + vec3(1.0, 1.0, 1.0));

    return mix(
      mix(mix(n000, n100, f.x), mix(n010, n110, f.x), f.y),
      mix(mix(n001, n101, f.x), mix(n011, n111, f.x), f.y),
      f.z
    );
  }

  void main() {
    vNormal = normalize(normalMatrix * normal);
    vec3 displaced = position;
    float field = noise(normalize(position) * 3.2 + vec3(0.0, 0.0, uTime * 0.32));
    float ripple = sin((position.y * 4.8) - (uTime * 1.15)) * 0.035;
    displaced += normal * ((field - 0.5) * 0.18 + ripple) * uPulse;

    vec4 world = modelMatrix * vec4(displaced, 1.0);
    vWorld = world.xyz;
    vNoise = field;

    gl_Position = projectionMatrix * viewMatrix * world;
  }
`;

const CORE_FRAGMENT = /* glsl */ `
  uniform float uTime;

  varying vec3 vWorld;
  varying vec3 vNormal;
  varying float vNoise;

  void main() {
    vec3 viewDir = normalize(cameraPosition - vWorld);
    float fresnel = pow(1.0 - max(dot(vNormal, viewDir), 0.0), 2.15);
    float pulse = 0.5 + 0.5 * sin(uTime * 1.2 + vNoise * 10.0);

    vec3 base = mix(vec3(0.04, 0.06, 0.09), vec3(0.08, 0.12, 0.18), vNoise);
    vec3 accent = mix(vec3(0.13, 0.78, 1.0), vec3(0.42, 0.53, 1.0), pulse);
    vec3 color = base + accent * (0.24 + fresnel * 0.85);

    float alpha = 0.86 + fresnel * 0.12;
    gl_FragColor = vec4(color, alpha);
  }
`;

const HALO_VERTEX = /* glsl */ `
  varying vec2 vUv;
  void main() {
    vUv = uv;
    gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
  }
`;

const HALO_FRAGMENT = /* glsl */ `
  uniform float uTime;
  varying vec2 vUv;

  float ring(float radius, float width, float dist) {
    return smoothstep(radius + width, radius, dist) - smoothstep(radius, radius - width, dist);
  }

  void main() {
    vec2 uv = vUv - 0.5;
    float dist = length(uv);
    float angle = atan(uv.y, uv.x);

    float pulse = 0.5 + 0.5 * sin(uTime * 1.4 + dist * 22.0);
    float rings = ring(0.18, 0.028, dist) * 0.95;
    rings += ring(0.29, 0.02, dist) * 0.58;
    rings += ring(0.39, 0.016, dist) * 0.34;
    rings += ring(0.49, 0.012, dist) * 0.18;

    float arc = smoothstep(0.18, 0.0, abs(sin(angle * 3.0 + uTime * 0.2)) - 0.35);
    arc *= smoothstep(0.52, 0.12, dist) * 0.22;

    vec3 color = mix(vec3(0.11, 0.58, 0.82), vec3(0.41, 0.55, 1.0), pulse);
    float alpha = (rings + arc) * smoothstep(0.68, 0.08, dist);

    gl_FragColor = vec4(color, alpha);
  }
`;

const HeroCanvas: Component<{ class?: string }> = (props) => {
  let containerRef!: HTMLDivElement;

  onMount(() => {
    const isMobile = window.innerWidth < 768;
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const geometries: THREE.BufferGeometry[] = [];
    const materials: THREE.Material[] = [];
    const resizeObservers: Array<IntersectionObserver | ResizeObserver> = [];

    let raf = 0;
    let isVisible = true;
    let isTabVisible = !document.hidden;
    let renderer: THREE.WebGLRenderer | null = null;
    let scene: THREE.Scene | null = null;
    let camera: THREE.PerspectiveCamera | null = null;
    let opticGroup: THREE.Group | null = null;
    let coreMaterial: THREE.ShaderMaterial | null = null;
    let haloMaterial: THREE.ShaderMaterial | null = null;
    let coreMesh: THREE.Mesh | null = null;
    let haloMesh: THREE.Mesh | null = null;
    let lensGlass: THREE.Mesh | null = null;
    let outerRing: THREE.Mesh | null = null;
    let innerRing: THREE.Mesh | null = null;
    let trackingArc: THREE.Mesh | null = null;
    let ringPlateOuter: THREE.Mesh | null = null;
    let ringPlateInner: THREE.Mesh | null = null;

    const pointer = { targetX: 0, targetY: 0, x: 0, y: 0 };

    const mountRenderer = () => {
      renderer = new THREE.WebGLRenderer({
        alpha: true,
        antialias: false,
        powerPreference: 'high-performance',
      });
      renderer.setClearColor(0x000000, 0);
      renderer.outputColorSpace = THREE.SRGBColorSpace;
      renderer.toneMapping = THREE.ACESFilmicToneMapping;
      renderer.toneMappingExposure = 1.15;

      const dpr = Math.min(window.devicePixelRatio, isMobile ? 1.2 : 1.5) * 0.75;
      renderer.setPixelRatio(dpr);
      renderer.domElement.style.width = '100%';
      renderer.domElement.style.height = '100%';
      renderer.domElement.style.display = 'block';
      containerRef.appendChild(renderer.domElement);
    };

    const registerGeometry = <T extends THREE.BufferGeometry>(geometry: T) => {
      geometries.push(geometry);
      return geometry;
    };

    const registerMaterial = <T extends THREE.Material>(material: T) => {
      materials.push(material);
      return material;
    };

    const buildScene = () => {
      if (!renderer) return;

      const rect = containerRef.getBoundingClientRect();
      scene = new THREE.Scene();
      camera = new THREE.PerspectiveCamera(34, rect.width / Math.max(rect.height, 1), 0.1, 40);
      camera.position.set(0, 0, isMobile ? 6.5 : 5.2);

      opticGroup = new THREE.Group();
      opticGroup.rotation.z = 0.08;
      scene.add(opticGroup);

      const ambient = new THREE.HemisphereLight(0xa8f2ff, 0x04080d, 1.15);
      const key = new THREE.DirectionalLight(0x7be0ff, 2.2);
      key.position.set(3, 2, 5);
      const fill = new THREE.PointLight(0x466dff, 1.3, 20, 2);
      fill.position.set(-3, -1.5, 2.5);
      scene.add(ambient, key, fill);

      coreMaterial = registerMaterial(
        new THREE.ShaderMaterial({
          vertexShader: CORE_VERTEX,
          fragmentShader: CORE_FRAGMENT,
          uniforms: {
            uTime: { value: 0 },
            uPulse: { value: reducedMotion ? 0.45 : 1 },
          },
          transparent: true,
        }),
      );

      coreMesh = new THREE.Mesh(
        registerGeometry(new THREE.IcosahedronGeometry(isMobile ? 0.68 : 0.76, isMobile ? 3 : 4)),
        coreMaterial,
      );
      opticGroup.add(coreMesh);

      haloMaterial = registerMaterial(
        new THREE.ShaderMaterial({
          vertexShader: HALO_VERTEX,
          fragmentShader: HALO_FRAGMENT,
          uniforms: {
            uTime: { value: 0 },
          },
          transparent: true,
          depthWrite: false,
          blending: THREE.AdditiveBlending,
          side: THREE.DoubleSide,
        }),
      );

      haloMesh = new THREE.Mesh(registerGeometry(new THREE.PlaneGeometry(4.1, 4.1)), haloMaterial);
      haloMesh.position.z = -0.05;
      opticGroup.add(haloMesh);

      lensGlass = new THREE.Mesh(
        registerGeometry(new THREE.CircleGeometry(isMobile ? 1.02 : 1.12, 96)),
        registerMaterial(
          new THREE.MeshPhysicalMaterial({
            color: 0x08111a,
            roughness: 0.08,
            metalness: 0.18,
            transmission: 0.34,
            transparent: true,
            opacity: 0.28,
            thickness: 0.8,
          }),
        ),
      );
      lensGlass.position.z = 0.06;
      opticGroup.add(lensGlass);

      outerRing = new THREE.Mesh(
        registerGeometry(new THREE.TorusGeometry(isMobile ? 1.28 : 1.42, 0.07, 20, 160)),
        registerMaterial(
          new THREE.MeshPhysicalMaterial({
            color: 0x0c1520,
            roughness: 0.19,
            metalness: 0.92,
            clearcoat: 0.68,
            reflectivity: 0.52,
          }),
        ),
      );
      outerRing.rotation.x = 1.48;
      outerRing.rotation.y = 0.08;
      opticGroup.add(outerRing);

      innerRing = new THREE.Mesh(
        registerGeometry(new THREE.TorusGeometry(isMobile ? 0.84 : 0.96, 0.035, 18, 140)),
        registerMaterial(
          new THREE.MeshPhysicalMaterial({
            color: 0x102030,
            roughness: 0.28,
            metalness: 0.86,
            emissive: 0x0a2942,
            emissiveIntensity: 0.2,
          }),
        ),
      );
      innerRing.rotation.x = 1.52;
      innerRing.rotation.y = 0.24;
      opticGroup.add(innerRing);

      const apertureRing = new THREE.Mesh(
        registerGeometry(new THREE.TorusGeometry(isMobile ? 0.56 : 0.62, 0.025, 12, 120)),
        registerMaterial(
          new THREE.MeshBasicMaterial({
            color: 0x7fe9ff,
            transparent: true,
            opacity: 0.32,
          }),
        ),
      );
      apertureRing.rotation.x = 1.54;
      opticGroup.add(apertureRing);

      trackingArc = new THREE.Mesh(
        registerGeometry(new THREE.TorusGeometry(isMobile ? 1.58 : 1.76, 0.009, 8, 180, Math.PI * 1.34)),
        registerMaterial(
          new THREE.MeshBasicMaterial({
            color: 0x7ddcff,
            transparent: true,
            opacity: 0.24,
          }),
        ),
      );
      trackingArc.rotation.z = 0.34;
      trackingArc.rotation.x = 0.18;
      opticGroup.add(trackingArc);

      ringPlateOuter = new THREE.Mesh(
        registerGeometry(new THREE.RingGeometry(1.12, 1.16, 180)),
        registerMaterial(
          new THREE.MeshBasicMaterial({
            color: 0x76d8ff,
            transparent: true,
            opacity: 0.14,
            side: THREE.DoubleSide,
          }),
        ),
      );
      ringPlateOuter.position.z = 0.12;
      opticGroup.add(ringPlateOuter);

      ringPlateInner = new THREE.Mesh(
        registerGeometry(new THREE.RingGeometry(0.58, 0.62, 180)),
        registerMaterial(
          new THREE.MeshBasicMaterial({
            color: 0x8eefff,
            transparent: true,
            opacity: 0.28,
            side: THREE.DoubleSide,
          }),
        ),
      );
      ringPlateInner.position.z = 0.2;
      opticGroup.add(ringPlateInner);

      renderer.setSize(rect.width, rect.height, false);
    };

    const resize = () => {
      if (!renderer || !camera) return;
      const rect = containerRef.getBoundingClientRect();
      const width = Math.max(rect.width, 1);
      const height = Math.max(rect.height, 1);

      renderer.setSize(width, height, false);
      camera.aspect = width / height;
      camera.updateProjectionMatrix();
    };

    const handleVisibility = () => {
      isTabVisible = !document.hidden;
    };

    const handlePointerMove = (event: PointerEvent) => {
      const rect = containerRef.getBoundingClientRect();
      const px = (event.clientX - rect.left) / Math.max(rect.width, 1);
      const py = (event.clientY - rect.top) / Math.max(rect.height, 1);
      pointer.targetX = (px - 0.5) * 0.24;
      pointer.targetY = (py - 0.5) * 0.18;
    };

    const resetPointer = () => {
      pointer.targetX = 0;
      pointer.targetY = 0;
    };

    const renderLoop = () => {
      raf = window.requestAnimationFrame(renderLoop);
      if (!renderer || !scene || !camera || !opticGroup || !coreMaterial || !haloMaterial) return;
      if (!isVisible || !isTabVisible) return;

      const time = performance.now() * 0.001;
      pointer.x += (pointer.targetX - pointer.x) * 0.055;
      pointer.y += (pointer.targetY - pointer.y) * 0.055;

      coreMaterial.uniforms.uTime.value = time;
      haloMaterial.uniforms.uTime.value = time;

      opticGroup.rotation.y += ((pointer.x + 0.04) - opticGroup.rotation.y) * 0.045;
      opticGroup.rotation.x += ((-pointer.y - 0.04) - opticGroup.rotation.x) * 0.045;
      opticGroup.rotation.z += (0.08 - opticGroup.rotation.z) * 0.04;
      opticGroup.position.y = Math.sin(time * 0.6) * (reducedMotion ? 0.015 : 0.05);

      if (coreMesh) {
        coreMesh.rotation.y += reducedMotion ? 0.0018 : 0.0038;
        coreMesh.rotation.x += reducedMotion ? 0.0012 : 0.0026;
      }
      if (haloMesh) haloMesh.rotation.z = time * 0.08;
      if (lensGlass) lensGlass.rotation.z = -time * 0.03;
      if (outerRing) outerRing.rotation.z = time * 0.11;
      if (innerRing) innerRing.rotation.z = -time * 0.18;
      if (trackingArc) trackingArc.rotation.z = 0.34 + Math.sin(time * 0.22) * 0.07;
      if (ringPlateOuter) ringPlateOuter.scale.setScalar(1 + Math.sin(time * 0.7) * 0.012);
      if (ringPlateInner) ringPlateInner.scale.setScalar(1 + Math.cos(time * 1.1) * 0.015);

      renderer.render(scene, camera);
    };

    mountRenderer();
    buildScene();
    resize();

    const visibilityObserver = new IntersectionObserver(
      ([entry]) => {
        isVisible = entry?.isIntersecting ?? true;
      },
      { threshold: 0.12 },
    );
    visibilityObserver.observe(containerRef);
    resizeObservers.push(visibilityObserver);

    const resizeObserver = new ResizeObserver(() => resize());
    resizeObserver.observe(containerRef);
    resizeObservers.push(resizeObserver);

    containerRef.addEventListener('pointermove', handlePointerMove);
    containerRef.addEventListener('pointerleave', resetPointer);
    document.addEventListener('visibilitychange', handleVisibility);
    window.addEventListener('resize', resize, { passive: true });

    renderLoop();

    onCleanup(() => {
      window.cancelAnimationFrame(raf);
      containerRef.removeEventListener('pointermove', handlePointerMove);
      containerRef.removeEventListener('pointerleave', resetPointer);
      document.removeEventListener('visibilitychange', handleVisibility);
      window.removeEventListener('resize', resize);

      resizeObservers.forEach((observer) => observer.disconnect());
      resizeObservers.length = 0;

      geometries.forEach((geometry) => geometry.dispose());
      materials.forEach((material) => material.dispose());

      scene?.clear();
      renderer?.dispose();
      renderer?.domElement.remove();
    });
  });

  return <div ref={containerRef!} class={props.class} aria-hidden="true" />;
};

export default HeroCanvas;
