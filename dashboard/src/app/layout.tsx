/**
 * Server component: the root layout.
 *
 * There is deliberately no client-side data fetching here. Every page is
 * rendered on the Go side of the wire boundary by the server component
 * runtime, which is what lets the whole thing be a static export.
 */
import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Lensyxe',
  description: 'Engineering health, measured locally.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="dark">
      <body className="min-h-screen bg-slate-950 text-slate-200 antialiased">
        <div className="mx-auto max-w-6xl px-6 py-10">
          <header className="mb-10 flex items-baseline justify-between border-b border-slate-800 pb-4">
            <div>
              <h1 className="text-xl font-semibold tracking-tight text-slate-100">
                Lensyxe
              </h1>
              <p className="text-sm text-slate-500">
                Engineering health, measured on your machine.
              </p>
            </div>
            <span className="text-xs text-slate-600">local-first</span>
          </header>
          <main>{children}</main>
          <footer className="mt-16 border-t border-slate-800 pt-4 text-xs text-slate-600">
            Every figure on this page comes from Lensyxe&rsquo;s own
            measurements. Nothing is estimated.
          </footer>
        </div>
      </body>
    </html>
  );
}