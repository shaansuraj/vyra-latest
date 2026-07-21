import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';

gsap.registerPlugin(ScrollTrigger);

export interface RevealOnEnterOptions {
  scope: Element;
  selector: string;
  trigger?: Element | null;
  start?: string;
  y?: number;
  duration?: number;
  stagger?: number;
  ease?: string;
}

export const revealOnEnter = (options: RevealOnEnterOptions) => {
  const targets = gsap.utils.toArray<HTMLElement>(options.selector, options.scope);
  if (!targets.length) return () => {};

  const cleanup = () => {
    gsap.killTweensOf(targets);
    gsap.set(targets, {
      clearProps: 'opacity,visibility,transform',
    });
  };

  const trigger = ScrollTrigger.create({
    trigger: options.trigger || options.scope,
    start: options.start || 'top 84%',
    once: true,
    onEnter: () => {
      gsap.fromTo(
        targets,
        {
          autoAlpha: 0,
          y: options.y ?? 28,
        },
        {
          autoAlpha: 1,
          y: 0,
          duration: options.duration ?? 0.82,
          stagger: options.stagger ?? 0.1,
          ease: options.ease ?? 'power3.out',
          overwrite: 'auto',
          clearProps: 'opacity,visibility,transform',
        }
      );
    },
  });

  return () => {
    trigger.kill();
    cleanup();
  };
};
