import { useState, useEffect } from 'react';

export function useSidebarResize() {
  const [leftSidebarWidth, setLeftSidebarWidth] = useState(320);
  const [rightSidebarWidth, setRightSidebarWidth] = useState(500);
  const [isLeftSidebarOpen, setIsLeftSidebarOpen] = useState(true);
  const [isRightSidebarOpen, setIsRightSidebarOpen] = useState(true);
  const [isResizingLeft, setIsResizingLeft] = useState(false);
  const [isResizingRight, setIsResizingRight] = useState(false);

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (isResizingLeft) {
        const newWidth = e.clientX;
        if (newWidth > 150 && newWidth < 600) {
          setLeftSidebarWidth(newWidth);
        }
      }
      if (isResizingRight) {
        const newWidth = window.innerWidth - e.clientX;
        if (newWidth > 150 && newWidth < 800) {
          setRightSidebarWidth(newWidth);
        }
      }
    };

    const handleMouseUp = () => {
      setIsResizingLeft(false);
      setIsResizingRight(false);
    };

    if (isResizingLeft || isResizingRight) {
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';
    } else {
      document.body.style.cursor = 'default';
      document.body.style.userSelect = 'auto';
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.cursor = 'default';
      document.body.style.userSelect = 'auto';
    };
  }, [isResizingLeft, isResizingRight]);

  const [leftSidebarSplit, setLeftSidebarSplit] = useState(75);
  const [rightSidebarSplit, setRightSidebarSplit] = useState(50);
  const [isResizingLeftVertical, setIsResizingLeftVertical] = useState(false);
  const [isResizingRightVertical, setIsResizingRightVertical] = useState(false);

  useEffect(() => {
    const handleMouseMoveVertical = (e: MouseEvent) => {
      if (isResizingLeftVertical) {
        const sidebarHeight = window.innerHeight;
        const newSplit = (e.clientY / sidebarHeight) * 100;
        if (newSplit > 20 && newSplit < 80) {
          setLeftSidebarSplit(newSplit);
        }
      }
      if (isResizingRightVertical) {
        const sidebarHeight = window.innerHeight;
        const newSplit = (e.clientY / sidebarHeight) * 100;
        if (newSplit > 20 && newSplit < 80) {
          setRightSidebarSplit(newSplit);
        }
      }
    };

    const handleMouseUpVertical = () => {
      setIsResizingLeftVertical(false);
      setIsResizingRightVertical(false);
    };

    if (isResizingLeftVertical || isResizingRightVertical) {
      document.addEventListener('mousemove', handleMouseMoveVertical);
      document.addEventListener('mouseup', handleMouseUpVertical);
      document.body.style.cursor = 'row-resize';
      document.body.style.userSelect = 'none';
    } else {
      if (!isResizingLeft && !isResizingRight) {
        document.body.style.cursor = 'default';
        document.body.style.userSelect = 'auto';
      }
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMoveVertical);
      document.removeEventListener('mouseup', handleMouseUpVertical);
      if (!isResizingLeft && !isResizingRight) {
        document.body.style.cursor = 'default';
        document.body.style.userSelect = 'auto';
      }
    };
  }, [isResizingLeftVertical, isResizingRightVertical, isResizingLeft, isResizingRight]);

  return {
    leftSidebarWidth,
    rightSidebarWidth,
    isLeftSidebarOpen,
    setIsLeftSidebarOpen,
    isRightSidebarOpen,
    setIsRightSidebarOpen,
    isResizingLeft,
    setIsResizingLeft,
    isResizingRight,
    setIsResizingRight,
    leftSidebarSplit,
    setLeftSidebarSplit,
    rightSidebarSplit,
    setRightSidebarSplit,
    setIsResizingLeftVertical,
    setIsResizingRightVertical,
    setLeftSidebarWidth,
    setRightSidebarWidth,
  };
}
