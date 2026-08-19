import React from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { Layout } from "./App";
import { Home } from "./pages/Home";
import { ProjectPage } from "./pages/Project";
import { BranchPage } from "./pages/Branch";
import { DiffPage } from "./pages/Diff";
import { MergePage } from "./pages/Merge";
import { MigrationPage } from "./pages/Migration";
import "./styles.css";

const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { path: "/", element: <Home /> },
      { path: "/projects/:projectId", element: <ProjectPage /> },
      { path: "/branches/:branchId", element: <BranchPage /> },
      { path: "/projects/:projectId/diff", element: <DiffPage /> },
      { path: "/projects/:projectId/merge", element: <MergePage /> },
      { path: "/projects/:projectId/migration", element: <MigrationPage /> },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
);
