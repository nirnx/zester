'use client';

import { useEffect, useId, useState } from 'react';
import { useTheme } from 'next-themes';

export function Mermaid({ chart }: { chart: string }) {
  const id = useId();
  const [svg, setSvg] = useState('');
  const { resolvedTheme } = useTheme();

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      const { default: mermaid } = await import('mermaid');
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'loose',
        fontFamily: 'inherit',
        themeCSS: 'margin: 1.5rem auto 0;',
        theme: resolvedTheme === 'dark' ? 'dark' : 'default',
      });
      try {
        const { svg: rendered } = await mermaid.render(
          id.replaceAll(':', ''),
          chart.replaceAll('\\n', '\n'),
        );
        if (!cancelled) setSvg(rendered);
      } catch (e) {
        console.error('mermaid render error', e);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [chart, id, resolvedTheme]);

  return <div dangerouslySetInnerHTML={{ __html: svg }} />;
}
