import type { Metadata } from "next";

import { AuthBootstrap } from "@/components/AuthBootstrap";
import { QueryProvider } from "@/components/QueryProvider";

import "./globals.css";

export const metadata: Metadata = {
  title: "Smartcore",
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
      <body className="antialiased">
        <QueryProvider>
          <AuthBootstrap />
          {children}
        </QueryProvider>
      </body>
    </html>
  );
}
