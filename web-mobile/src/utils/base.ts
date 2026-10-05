// 应用 base 探测：生产经网关 /m/* 挂载（router base '/m/'），dev 在根路径。
// vite 资产 base 与路由 base 是两件事：资产 base 由构建期决定（build=
// '/m-assets/'，dev='/'），路由 base 由部署位置运行时决定。
export function appBase(): '/m/' | '/' {
  if (typeof window !== 'undefined' && window.location.pathname.split('/')[1] === 'm') {
    return '/m/'
  }
  return '/'
}
