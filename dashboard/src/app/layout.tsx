/**
 * Server component: the root layout.
 *
 * There is deliberately no client-side data fetching here. Every page is
 * rendered on the Go side of the wire boundary by the server component
 * runtime, which is what lets the whole thing be a static export.
 */
import type { Metadata, Viewport } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Lensyxe',
  description: 'Engineering health, measured locally.',
};

// Without this a mobile browser lays the page out at ~980px and scales it down,
// which makes every size in the stylesheet wrong by exactly that factor.
export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  themeColor: '#020617',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="dark">
      <body className="min-h-screen bg-slate-950 text-slate-200 antialiased">
        {/*
          Padding and width step at the sm breakpoint rather than being fixed.
          A 24px gutter is comfortable on a desktop and wastes a third of a
          360px phone, and max-w-6xl alone leaves a tablet looking like a wide
          desktop page with the same margins.
        */}
        <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 sm:py-8 lg:px-8 lg:py-10">
          <header className="mb-6 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-b border-slate-800 pb-4 sm:mb-8">
            <div className="min-w-0">
              <h1 className="text-lg font-semibold tracking-tight text-slate-100 sm:text-xl">
                Lensyxe
              </h1>
              <p className="text-xs text-slate-300 sm:text-sm">
                Engineering health, measured on your machine.
              </p>
            </div>
            <span className="shrink-0 text-xs text-slate-400">local-first</span>
          </header>
          <main>{children}</main>
          <footer className="mt-10 border-t border-slate-800 pt-4 text-xs leading-relaxed text-slate-400 sm:mt-16">
            Every figure on this page comes from Lensyxe&rsquo;s own
            measurements. Nothing is estimated.
          </footer>
        </div>
      </body>
    </html>
  );
}