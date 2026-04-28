import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "WorkTrack — Internal RMM",
  description: "Internal endpoint monitoring and management",
  robots: { index: false, follow: false },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="vi" suppressHydrationWarning>
      <body className="antialiased">{children}</body>
    </html>
  );
}
