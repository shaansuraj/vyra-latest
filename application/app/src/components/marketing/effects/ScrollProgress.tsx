import { type Component, onMount, onCleanup, createSignal } from 'solid-js';

const ScrollProgress: Component = () => {
  const [progress, setProgress] = createSignal(0);

  onMount(() => {
    const onScroll = () => {
      const scrollTop = window.scrollY;
      const docHeight = document.documentElement.scrollHeight - window.innerHeight;
      setProgress(docHeight > 0 ? (scrollTop / docHeight) * 100 : 0);
    };

    window.addEventListener('scroll', onScroll, { passive: true });
    onCleanup(() => window.removeEventListener('scroll', onScroll));
  });

  return (
    <div class="v3-scroll-progress" aria-hidden="true">
      <div class="v3-scroll-progress-bar" style={{ transform: `scaleX(${progress() / 100})` }} />
    </div>
  );
};

export default ScrollProgress;
