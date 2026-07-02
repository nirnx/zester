import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { gitConfig } from './shared';

export function ZesterMark({ size = 22 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      aria-hidden
      className="text-fd-primary"
    >
      <circle
        cx="16"
        cy="16"
        r="13.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="3"
      />
      <path d="M16 16 L16 5.5 A10.5 10.5 0 0 1 26.5 16 Z" fill="currentColor" />
      <path
        d="M16 16 L5.5 16 A10.5 10.5 0 0 0 16 26.5 Z"
        fill="currentColor"
        opacity="0.45"
      />
    </svg>
  );
}

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <>
          <ZesterMark />
          <span className="font-display font-semibold text-[15px] tracking-tight">
            zester
          </span>
        </>
      ),
    },
    links: [
      { text: 'Docs', url: '/docs' },
      { text: 'Modules', url: '/docs/guides/modules' },
      { text: 'Operations', url: '/docs/operations' },
      { text: 'Architecture', url: '/docs/architecture' },
    ],
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
