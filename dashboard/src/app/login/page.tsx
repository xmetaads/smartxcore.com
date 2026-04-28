export default function LoginPage() {
  return (
    <main className="flex min-h-screen items-center justify-center bg-slate-50 px-6">
      <div className="w-full max-w-md space-y-6 rounded-lg border bg-white p-8 shadow-sm">
        <div className="space-y-2">
          <h1 className="text-xl font-semibold">Đăng nhập quản trị</h1>
          <p className="text-sm text-slate-500">
            Sử dụng email công ty và mật khẩu được cấp.
          </p>
        </div>

        <form className="space-y-4" action="/api/auth/login" method="POST">
          <div className="space-y-1">
            <label htmlFor="email" className="block text-sm font-medium text-slate-700">
              Email
            </label>
            <input
              id="email"
              name="email"
              type="email"
              required
              autoComplete="email"
              className="block w-full rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
            />
          </div>

          <div className="space-y-1">
            <label htmlFor="password" className="block text-sm font-medium text-slate-700">
              Mật khẩu
            </label>
            <input
              id="password"
              name="password"
              type="password"
              required
              autoComplete="current-password"
              className="block w-full rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
            />
          </div>

          <button
            type="submit"
            className="w-full rounded-md bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-800"
          >
            Đăng nhập
          </button>
        </form>

        <p className="text-center text-xs text-slate-400">
          Hệ thống yêu cầu xác thực 2 bước (TOTP) sau lần đăng nhập đầu tiên.
        </p>
      </div>
    </main>
  );
}
